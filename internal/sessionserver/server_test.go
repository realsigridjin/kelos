package sessionserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestAuthenticationProtectsApplicationAndAPI(t *testing.T) {
	server := testServer(t)

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusFound || response.Header().Get("Location") != "/login" {
		t.Fatalf("GET / status = %d location = %q", response.Code, response.Header().Get("Location"))
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"wrong"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d, want %d", response.Code, http.StatusUnauthorized)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"secret-token"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid login status = %d body = %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("authentication cookie = %#v", cookies)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Kelos Sessions") {
		t.Fatalf("authenticated application status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionFormUsesResourceSelectors(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET / status = %d body = %s", response.Code, response.Body.String())
	}
	for _, expected := range []string{
		`id="credential-secret"`,
		`id="workspace-select"`,
		`id="agent-config-select"`,
		`id="selected-agent-configs"`,
		`<option value="opencode">OpenCode</option>`,
	} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("new Session form does not contain %s", expected)
		}
	}
}

func TestSessionAPIHappyPath(t *testing.T) {
	server := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"defaultNamespace":"default"`) {
		t.Fatalf("config status = %d body = %s", response.Code, response.Body.String())
	}

	payload := map[string]any{
		"name":      "chat",
		"namespace": "default",
		"worker": map[string]any{
			"type":        "codex",
			"credentials": map[string]string{"type": "none"},
		},
	}
	body, _ := json.Marshal(payload)
	request = httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
	}
	var sessions []sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Name != "chat" || sessions[0].Provider != "codex" {
		t.Fatalf("listed Sessions = %#v", sessions)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/sessions/default/chat", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestSessionAPIRejectsOtherNamespacesAndUnsafeWorkerFields(t *testing.T) {
	server := testServer(t)
	for _, test := range []struct {
		name       string
		payload    string
		wantStatus int
	}{
		{
			name:       "other namespace",
			payload:    `{"name":"chat","namespace":"team-a","worker":{"type":"codex","credentials":{"type":"none"}}}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "custom image",
			payload:    `{"name":"chat","namespace":"default","worker":{"type":"codex","credentials":{"type":"none"},"image":"example.invalid/unsafe:latest"}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "pod overrides",
			payload:    `{"name":"chat","namespace":"default","worker":{"type":"codex","credentials":{"type":"none"},"podOverrides":{"serviceAccountName":"kelos-controller"}}}`,
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader(test.payload))
			request.Header.Set("Authorization", "Bearer secret-token")
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/team-a/chat", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-namespace get status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestSessionAPIListsOnlyConfiguredNamespace(t *testing.T) {
	server := testServer(t)
	for _, namespace := range []string{"default", "team-a"} {
		session := &kelos.Session{
			ObjectMeta: metav1.ObjectMeta{Name: "chat-" + namespace, Namespace: namespace},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		}
		if err := server.client.Create(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", response.Code, response.Body.String())
	}
	var sessions []sessionSummary
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].Namespace != "default" {
		t.Fatalf("listed Sessions = %#v", sessions)
	}
}

func TestSessionOptionsAPI(t *testing.T) {
	server := testServer(t)
	for _, workspace := range []kelos.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "zeta", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &workspace); err != nil {
			t.Fatal(err)
		}
	}
	for _, agentConfig := range []kelos.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "tools", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "defaults", Namespace: "default"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"}},
	} {
		if err := server.client.Create(t.Context(), &agentConfig); err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "codex-duplicate", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "claude", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "claude-code",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeAPIKey,
					SecretRef: &kelos.SecretReference{Name: "claude-credentials"},
				},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "none", Namespace: "default"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "codex",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
			}},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "team-a"},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type: "codex",
				Credentials: &kelos.Credentials{
					Type:      kelos.CredentialTypeOAuth,
					SecretRef: &kelos.SecretReference{Name: "other-credentials"},
				},
			}},
		},
	} {
		if err := server.client.Create(t.Context(), &session); err != nil {
			t.Fatal(err)
		}
	}

	request := httptest.NewRequest(http.MethodGet, "/api/options", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("options status = %d body = %s", response.Code, response.Body.String())
	}
	var options sessionOptions
	if err := json.Unmarshal(response.Body.Bytes(), &options); err != nil {
		t.Fatal(err)
	}
	wantCredentials := []credentialOption{
		{Name: "claude-credentials", Type: kelos.CredentialTypeAPIKey, Provider: "claude-code"},
		{Name: "codex-credentials", Type: kelos.CredentialTypeOAuth, Provider: "codex"},
	}
	if len(options.Credentials) != len(wantCredentials) {
		t.Fatalf("credential options = %#v, want %#v", options.Credentials, wantCredentials)
	}
	for i := range wantCredentials {
		if options.Credentials[i] != wantCredentials[i] {
			t.Errorf("credential option %d = %#v, want %#v", i, options.Credentials[i], wantCredentials[i])
		}
	}
	if got := strings.Join(options.Workspaces, ","); got != "alpha,zeta" {
		t.Errorf("workspace options = %q, want %q", got, "alpha,zeta")
	}
	if got := strings.Join(options.AgentConfigs, ","); got != "defaults,tools" {
		t.Errorf("AgentConfig options = %q, want %q", got, "defaults,tools")
	}
}

func TestNewRejectsEmptyToken(t *testing.T) {
	for _, token := range []string{"", " \t"} {
		_, err := New(Config{Token: token})
		if err == nil || !strings.Contains(err.Error(), "must not be empty") {
			t.Fatalf("New() token %q error = %v", token, err)
		}
	}
}

func TestNewRejectsEmptyDefaultNamespace(t *testing.T) {
	_, err := New(Config{Token: "secret-token"})
	if err == nil || !strings.Contains(err.Error(), "namespace must not be empty") {
		t.Fatalf("New() error = %v", err)
	}
}

func TestConnectSessionBridgesReadySession(t *testing.T) {
	server := testServer(t)
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "chat", Namespace: "default"},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type:        "codex",
			Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
		}},
		Status: kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "chat-pod"},
	}
	if err := server.client.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	bridged := make(chan struct{})
	server.bridge = func(_ context.Context, connection *sessionSocket, namespace, podName string) error {
		defer close(bridged)
		if namespace != "default" || podName != "chat-pod" {
			t.Errorf("bridge target = %s/%s, want default/chat-pod", namespace, podName)
		}
		var request map[string]any
		if err := connection.ReadJSON(&request); err != nil {
			return err
		}
		if request["type"] != "subscribe" {
			t.Errorf("bridge request type = %v, want subscribe", request["type"])
		}
		return connection.WriteJSON(map[string]any{"type": "history.end"})
	}

	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	header := http.Header{"Authorization": []string{"Bearer secret-token"}}
	connection, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/sessions/default/chat/connect", header)
	if err != nil {
		if response != nil {
			t.Fatalf("connecting WebSocket: %v (status %d)", err, response.StatusCode)
		}
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.WriteJSON(map[string]any{"type": "subscribe", "since": 0}); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := connection.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "history.end" {
		t.Fatalf("event type = %v, want history.end", event["type"])
	}
	select {
	case <-bridged:
	case <-time.After(time.Second):
		t.Fatal("bridge did not complete")
	}
}

func TestSessionSocketSerializesWrites(t *testing.T) {
	const writes = 32
	serverDone := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			serverDone <- err
			return
		}
		socket := &sessionSocket{Conn: connection}
		defer socket.Close()
		var wait sync.WaitGroup
		errors := make(chan error, writes)
		for i := 0; i < writes; i++ {
			wait.Add(1)
			go func(value int) {
				defer wait.Done()
				errors <- socket.WriteJSON(map[string]int{"value": value})
			}(i)
		}
		wait.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}))
	defer httpServer.Close()

	connection, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for i := 0; i < writes; i++ {
		var message map[string]int
		if err := connection.ReadJSON(&message); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("concurrent writes failed: %v", err)
	}
}

func testServer(t *testing.T) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	controllerClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	server, err := New(Config{
		Token:            "secret-token",
		Client:           controllerClient,
		Clientset:        &kubernetes.Clientset{},
		RESTConfig:       &rest.Config{Host: "https://kubernetes.invalid"},
		DefaultNamespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
