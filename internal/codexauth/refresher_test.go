package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestRunRefreshesLabeledOAuthSecret(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "codex",
			Labels: map[string]string{
				RefreshLabel: "true",
			},
		},
		Data: map[string][]byte{
			secretKey: []byte(`{"tokens":{"access_token":"old","id_token":"id","refresh_token":"refresh"},"last_refresh":"2026-01-01T00:00:00Z"}`),
			"other":   []byte("kept"),
		},
	})

	var seeded map[string]any
	err := Run(ctx, clientset, Options{
		Namespace:  "default",
		SecretName: "codex",
		Runner: func(_ context.Context, raw []byte) ([]byte, error) {
			if err := json.Unmarshal(raw, &seeded); err != nil {
				t.Fatalf("seeded auth is invalid JSON: %v", err)
			}
			return []byte(`{"tokens":{"access_token":"new","id_token":"id","refresh_token":"refresh"},"last_refresh":"2026-06-02T00:00:00Z"}`), nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if seeded["last_refresh"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("seeded last_refresh = %v, want original value", seeded["last_refresh"])
	}
	updated, err := clientset.CoreV1().Secrets("default").Get(ctx, "codex", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting Secret: %v", err)
	}
	var gotAuth map[string]any
	if err := json.Unmarshal(updated.Data[secretKey], &gotAuth); err != nil {
		t.Fatalf("updated auth is invalid JSON: %v", err)
	}
	gotTokens, ok := gotAuth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("updated auth does not contain tokens")
	}
	if gotTokens["access_token"] != "new" {
		t.Fatalf("access_token = %v, want new", gotTokens["access_token"])
	}
	if gotAuth["last_refresh"] != "2026-06-02T00:00:00Z" {
		t.Fatalf("last_refresh = %v, want refreshed value", gotAuth["last_refresh"])
	}
	if got := string(updated.Data["other"]); got != "kept" {
		t.Fatalf("other key = %q, want kept", got)
	}
}

func TestRunSkipsSecretsWithoutRefreshToken(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "codex",
			Labels: map[string]string{
				RefreshLabel: "true",
			},
		},
		Data: map[string][]byte{
			secretKey: []byte(`{"tokens":{"access_token":"old"}}`),
		},
	})

	called := false
	err := Run(ctx, clientset, Options{
		Namespace:  "default",
		SecretName: "codex",
		Runner: func(context.Context, []byte) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("Runner was called for a bundle without refresh_token")
	}
}

func TestRunSkipsSecretsWithoutRefreshLabel(t *testing.T) {
	ctx := context.Background()
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "codex",
		},
		Data: map[string][]byte{
			secretKey: []byte(`{"tokens":{"access_token":"old","refresh_token":"refresh"}}`),
		},
	})

	called := false
	err := Run(ctx, clientset, Options{
		Namespace:  "default",
		SecretName: "codex",
		Runner: func(context.Context, []byte) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if called {
		t.Fatal("Runner was called for an unlabeled Secret")
	}
}

func TestRunUpdatesSecretWhenRefreshedPayloadHasNoMarker(t *testing.T) {
	ctx := context.Background()
	originalAuth := `{"tokens":{"access_token":"old","id_token":"id","refresh_token":"refresh"},"last_refresh":"2026-01-01T00:00:00Z"}`
	clientset := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "codex",
			Labels: map[string]string{
				RefreshLabel: "true",
			},
		},
		Data: map[string][]byte{
			secretKey: []byte(originalAuth),
		},
	})

	err := Run(ctx, clientset, Options{
		Namespace:  "default",
		SecretName: "codex",
		Runner: func(context.Context, []byte) ([]byte, error) {
			return []byte(`{"tokens":{"access_token":"new","id_token":"id","refresh_token":"refresh"}}`), nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	updated, err := clientset.CoreV1().Secrets("default").Get(ctx, "codex", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting Secret: %v", err)
	}
	if got := string(updated.Data[secretKey]); got == originalAuth {
		t.Fatalf("updated auth = %s, want changed payload", got)
	}

	var gotAuth map[string]any
	if err := json.Unmarshal(updated.Data[secretKey], &gotAuth); err != nil {
		t.Fatalf("updated auth is invalid JSON: %v", err)
	}
	gotTokens, ok := gotAuth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("updated auth does not contain tokens")
	}
	if gotTokens["access_token"] != "new" {
		t.Fatalf("access_token = %v, want new", gotTokens["access_token"])
	}
}

func TestRefreshWithOAuthTokenUsesRefreshTokenEndpoint(t *testing.T) {
	ctx := context.Background()
	var requestMethod string
	var requestContentType string
	var requestPath string
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMethod = r.Method
		requestContentType = r.Header.Get("Content-Type")
		requestPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		requestBody = string(body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new","id_token":"new-id","refresh_token":"new-refresh","token_type":"Bearer","scope":"openid","expires_in":3600}`))
	}))
	defer server.Close()

	refreshed, err := refreshWithOAuthToken(ctx, []byte(`{"client_id":"test-client","tokens":{"access_token":"old","id_token":"old-id","refresh_token":"refresh","token_type":"Bearer","expires_at":"old-expiry"},"last_refresh":"2026-01-01T00:00:00Z"}`), server.URL+"/oauth/token", server.Client())
	if err != nil {
		t.Fatalf("refreshWithOAuthToken() error = %v", err)
	}
	if requestMethod != http.MethodPost {
		t.Fatalf("request method = %q, want %q", requestMethod, http.MethodPost)
	}
	if requestContentType != oauthContentType {
		t.Fatalf("request Content-Type = %q, want %s", requestContentType, oauthContentType)
	}
	if requestPath != "/oauth/token" {
		t.Fatalf("request path = %q, want /oauth/token", requestPath)
	}

	requestForm, err := url.ParseQuery(requestBody)
	if err != nil {
		t.Fatalf("parsing refresh request form: %v", err)
	}
	if got := requestForm.Get("grant_type"); got != "refresh_token" {
		t.Fatalf("grant_type = %q, want %q", got, "refresh_token")
	}
	if got := requestForm.Get("refresh_token"); got != "refresh" {
		t.Fatalf("refresh_token = %q, want %q", got, "refresh")
	}
	if got := requestForm.Get("client_id"); got != "test-client" {
		t.Fatalf("client_id = %q, want %q", got, "test-client")
	}

	var updated map[string]any
	if err := json.Unmarshal(refreshed, &updated); err != nil {
		t.Fatalf("refreshed auth is invalid JSON: %v", err)
	}
	updatedTokens, ok := updated["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("refreshed auth does not contain tokens")
	}
	if updatedTokens["access_token"] != "new" {
		t.Fatalf("access_token = %v, want %v", updatedTokens["access_token"], "new")
	}
	if updatedTokens["id_token"] != "new-id" {
		t.Fatalf("id_token = %v, want %v", updatedTokens["id_token"], "new-id")
	}
	if updatedTokens["refresh_token"] != "new-refresh" {
		t.Fatalf("refresh_token = %v, want %v", updatedTokens["refresh_token"], "new-refresh")
	}
	if updatedTokens["token_type"] != "Bearer" {
		t.Fatalf("token_type = %v, want %v", updatedTokens["token_type"], "Bearer")
	}
	if _, ok := updatedTokens["expires_at"]; ok {
		t.Fatalf("expires_at = %v, want removed when response only has expires_in", updatedTokens["expires_at"])
	}
	if updatedTokens["expires_in"] != float64(3600) {
		t.Fatalf("expires_in = %v, want %v", updatedTokens["expires_in"], float64(3600))
	}
	lastRefresh, ok := updated["last_refresh"].(string)
	if !ok {
		t.Fatalf("last_refresh has unexpected type %T", updated["last_refresh"])
	}
	if _, err := time.Parse(time.RFC3339, lastRefresh); err != nil {
		t.Fatalf("last_refresh = %v, want RFC3339 timestamp: %v", lastRefresh, err)
	}
	if lastRefresh == "2026-01-01T00:00:00Z" {
		t.Fatalf("last_refresh was not refreshed")
	}
}

func TestRefreshWithOAuthTokenErrorsWhenAccessTokenMissing(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"refresh_token":"new-refresh"}`))
	}))
	defer server.Close()

	_, err := refreshWithOAuthToken(ctx, []byte(`{"client_id":"test-client","tokens":{"access_token":"old","refresh_token":"refresh"},"last_refresh":"2026-01-01T00:00:00Z"}`), server.URL+"/oauth/token", server.Client())
	if err == nil {
		t.Fatal("refreshWithOAuthToken() succeeded without access_token")
	}
	if !strings.Contains(err.Error(), "missing access_token") {
		t.Fatalf("error = %v, want message to mention missing access_token", err)
	}
}

func TestRefreshWithOAuthTokenUsesDefaultClientID(t *testing.T) {
	ctx := context.Background()
	var requestBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		requestBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new","id_token":"new-id","refresh_token":"new-refresh","token_type":"Bearer"}`))
	}))
	defer server.Close()

	_, err := refreshWithOAuthToken(ctx, []byte(`{"tokens":{"access_token":"old","refresh_token":"refresh"},"last_refresh":"2026-01-01T00:00:00Z"}`), server.URL+"/oauth/token", server.Client())
	if err != nil {
		t.Fatalf("refreshWithOAuthToken() error = %v", err)
	}
	requestForm, err := url.ParseQuery(requestBody)
	if err != nil {
		t.Fatalf("parsing refresh request form: %v", err)
	}
	if got := requestForm.Get("client_id"); got != defaultOAuthClientID {
		t.Fatalf("client_id = %q, want %q", got, defaultOAuthClientID)
	}
}

func TestRefreshWithOAuthTokenRefreshesEveryCall(t *testing.T) {
	ctx := context.Background()
	var refreshTokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading request body: %v", err)
		}
		requestForm, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatalf("parsing refresh request form: %v", err)
		}
		refreshTokens = append(refreshTokens, requestForm.Get("refresh_token"))
		count := len(refreshTokens)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","token_type":"Bearer","expires_in":3600}`, count, count+1)
	}))
	defer server.Close()

	first, err := refreshWithOAuthToken(ctx, []byte(`{"tokens":{"access_token":"old","refresh_token":"refresh-1"},"last_refresh":"2026-01-01T00:00:00Z"}`), server.URL+"/oauth/token", server.Client())
	if err != nil {
		t.Fatalf("first refreshWithOAuthToken() error = %v", err)
	}
	second, err := refreshWithOAuthToken(ctx, first, server.URL+"/oauth/token", server.Client())
	if err != nil {
		t.Fatalf("second refreshWithOAuthToken() error = %v", err)
	}

	if len(refreshTokens) != 2 {
		t.Fatalf("refresh request count = %d, want 2", len(refreshTokens))
	}
	if refreshTokens[0] != "refresh-1" {
		t.Fatalf("first refresh_token = %q, want refresh-1", refreshTokens[0])
	}
	if refreshTokens[1] != "refresh-2" {
		t.Fatalf("second refresh_token = %q, want refresh-2", refreshTokens[1])
	}

	var updated map[string]any
	if err := json.Unmarshal(second, &updated); err != nil {
		t.Fatalf("second refreshed auth is invalid JSON: %v", err)
	}
	updatedTokens, ok := updated["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("second refreshed auth does not contain tokens")
	}
	if updatedTokens["access_token"] != "access-2" {
		t.Fatalf("access_token = %v, want access-2", updatedTokens["access_token"])
	}
}
