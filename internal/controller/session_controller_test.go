package controller

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

func TestSessionReconcilerCreatesAndObservesPod(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	session := testSession("chat", "claude-code")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{
		{Name: "KELOS_SESSION_SETUP_ONLY", Value: "1"},
	}}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}).
		WithObjects(session).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var pod corev1.Pod
	if err := cl.Get(context.Background(), request.NamespacedName, &pod); err != nil {
		t.Fatalf("getting Session Pod: %v", err)
	}
	if !metav1.IsControlledBy(&pod, session) {
		t.Fatal("Session Pod does not have the Session as controller owner")
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyAlways {
		t.Fatalf("restartPolicy = %q, want %q", pod.Spec.RestartPolicy, corev1.RestartPolicyAlways)
	}
	if got := pod.Labels["kelos.dev/session"]; got != session.Name {
		t.Fatalf("kelos.dev/session label = %q, want %q", got, session.Name)
	}
	if _, exists := pod.Labels["kelos.dev/task"]; exists {
		t.Fatal("Session Pod retained the Task label")
	}
	if len(pod.Spec.Containers) == 0 || len(pod.Spec.Containers[0].Command) != 1 || pod.Spec.Containers[0].Command[0] != sessionRuntimeBinary {
		t.Fatalf("agent command = %v, want %q", pod.Spec.Containers[0].Command, sessionRuntimeBinary)
	}
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.FSGroup == nil || *pod.Spec.SecurityContext.FSGroup != AgentUID {
		t.Fatalf("pod FSGroup = %#v, want %d", pod.Spec.SecurityContext, AgentUID)
	}
	if len(pod.Spec.InitContainers) == 0 || pod.Spec.InitContainers[0].Image != "runtime:test" {
		t.Fatalf("runtime init container = %#v", pod.Spec.InitContainers)
	}
	runtimeSecurity := pod.Spec.InitContainers[0].SecurityContext
	if runtimeSecurity == nil || runtimeSecurity.AllowPrivilegeEscalation == nil || *runtimeSecurity.AllowPrivilegeEscalation ||
		runtimeSecurity.RunAsNonRoot == nil || !*runtimeSecurity.RunAsNonRoot ||
		runtimeSecurity.SeccompProfile == nil || runtimeSecurity.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault ||
		runtimeSecurity.Capabilities == nil || len(runtimeSecurity.Capabilities.Drop) != 1 || runtimeSecurity.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("runtime init container securityContext = %#v", runtimeSecurity)
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatalf("automountServiceAccountToken = %v, want false", pod.Spec.AutomountServiceAccountToken)
	}
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "KELOS_SESSION_SETUP_ONLY" {
			t.Fatalf("reserved KELOS_SESSION_SETUP_ONLY reached the Session container with value %q", env.Value)
		}
	}

	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := cl.Status().Update(context.Background(), &pod); err != nil {
		t.Fatalf("updating Pod status: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() ready error = %v", err)
	}

	var updated kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.SessionPhaseReady {
		t.Fatalf("Session phase = %q, want %q", updated.Status.Phase, kelos.SessionPhaseReady)
	}
	if updated.Status.PodName != session.Name {
		t.Fatalf("Session podName = %q, want %q", updated.Status.PodName, session.Name)
	}
	if len(updated.Status.Conditions) != 1 || updated.Status.Conditions[0].Type != sessionReadyCondition || updated.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("Session conditions = %#v", updated.Status.Conditions)
	}
}

func TestSessionCodexPodUsesPersistentCodexHome(t *testing.T) {
	t.Parallel()
	session := testSession("codex-chat", "codex")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{{Name: "CODEX_HOME", Value: "/tmp/ignored"}}}
	reconciler := testSessionReconciler(nil, nil)
	pod, _, err := reconciler.buildSessionPod(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "CODEX_HOME" {
			if env.Value != sessionCodexHome || env.ValueFrom != nil {
				t.Fatalf("CODEX_HOME = %#v, want %q", env, sessionCodexHome)
			}
			return
		}
	}
	t.Fatal("CODEX_HOME was not injected")
}

func TestSessionClaudePodUsesPersistentConfig(t *testing.T) {
	t.Parallel()
	session := testSession("claude-chat", "claude-code")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{{Name: "CLAUDE_CONFIG_DIR", Value: "/tmp/ignored"}}}
	reconciler := testSessionReconciler(nil, nil)
	pod, _, err := reconciler.buildSessionPod(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "CLAUDE_CONFIG_DIR" {
			if env.Value != sessionClaudeConfigDir || env.ValueFrom != nil {
				t.Fatalf("CLAUDE_CONFIG_DIR = %#v, want %q", env, sessionClaudeConfigDir)
			}
			return
		}
	}
	t.Fatal("CLAUDE_CONFIG_DIR was not injected")
}

func TestSessionOpenCodePodUsesPersistentDirectories(t *testing.T) {
	t.Parallel()
	session := testSession("opencode-chat", "opencode")
	session.Spec.Worker.PodOverrides = &kelos.PodOverrides{Env: []corev1.EnvVar{
		{Name: "OPENCODE_CONFIG_DIR", Value: "/tmp/ignored"},
		{Name: "XDG_DATA_HOME", Value: "/tmp/ignored"},
	}}
	reconciler := testSessionReconciler(nil, nil)
	pod, _, err := reconciler.buildSessionPod(session, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPENCODE_CONFIG_DIR": sessionOpenCodeConfigDir,
		"XDG_DATA_HOME":       sessionOpenCodeDataDir,
	}
	for _, env := range pod.Spec.Containers[0].Env {
		if value, exists := want[env.Name]; exists {
			if env.Value != value || env.ValueFrom != nil {
				t.Fatalf("%s = %#v, want %q", env.Name, env, value)
			}
			delete(want, env.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("OpenCode Session environment is missing %v", want)
	}
}

func TestSessionPluginConfigMapUsesSessionIdentity(t *testing.T) {
	t.Parallel()
	session := testSession("shared-name", "claude-code")
	agentConfig := &kelos.AgentConfigSpec{Plugins: []kelos.PluginSpec{{
		Name:   "tools",
		Skills: []kelos.SkillDefinition{{Name: "review", Content: "Review changes"}},
	}}}
	reconciler := testSessionReconciler(nil, nil)
	pod, configMap, err := reconciler.buildSessionPod(session, nil, agentConfig)
	if err != nil {
		t.Fatal(err)
	}
	if configMap == nil {
		t.Fatal("Session plugin ConfigMap was not built")
	}
	if configMap.Name == PluginConfigMapName(session.Name) {
		t.Fatalf("Session plugin ConfigMap reused Task name %q", configMap.Name)
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == PluginStagingVolumeName && volume.ConfigMap != nil {
			if volume.ConfigMap.Name != configMap.Name {
				t.Fatalf("plugin volume ConfigMap = %q, want %q", volume.ConfigMap.Name, configMap.Name)
			}
			return
		}
	}
	t.Fatal("Session Pod has no plugin ConfigMap volume")
}

func TestSessionLabelValueBoundsLongNames(t *testing.T) {
	t.Parallel()
	session := testSession(strings.Repeat("a", 80), "codex")
	session.UID = types.UID("6d693cca-eace-4a0c-bf53-f2ea763c9b1f")
	if got := sessionLabelValue(session); got != string(session.UID) {
		t.Fatalf("sessionLabelValue() = %q, want UID %q", got, session.UID)
	}
	session.UID = ""
	if got := sessionLabelValue(session); len(got) > 63 {
		t.Fatalf("fallback label length = %d, want at most 63", len(got))
	}
}

func TestSessionGitHubTokenSecretNameBoundsLongNames(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("a", 253)
	got := sessionGitHubTokenSecretName(name)
	if len(got) > 63 {
		t.Fatalf("token Secret name length = %d, want at most 63", len(got))
	}
	if got != sessionGitHubTokenSecretName(name) {
		t.Fatal("token Secret name is not deterministic")
	}
	if got == sessionGitHubTokenSecretName(strings.Repeat("b", 253)) {
		t.Fatal("different Session names produced the same token Secret name")
	}
}

func TestSessionReconcilerRecreatesMissingGitHubTokenSecret(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	expiresAt := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"token":      "ghs_recreated",
			"expires_at": expiresAt.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kelos.AddToScheme(scheme)
	session := testSession("recover-token", "codex")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "workspace"}
	workspace := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: session.Namespace},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://github.com/kelos-dev/kelos.git",
			SecretRef: &kelos.SecretReference{Name: "github-app"},
		},
	}
	source := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "github-app", Namespace: session.Namespace},
		Data: map[string][]byte{
			"appID":          []byte("12345"),
			"installationID": []byte("67890"),
			"privateKey":     keyPEM,
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      session.Name,
			Namespace: session.Namespace,
			UID:       types.UID("pod-uid"),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(session, kelos.GroupVersion.WithKind("Session")),
			},
		},
		Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
			Name: GitHubTokenVolumeName,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName: sessionGitHubTokenSecretName(session.Name),
			}},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.Session{}, &corev1.Pod{}).
		WithObjects(session, workspace, source, pod).
		Build()
	reconciler := testSessionReconciler(cl, scheme)
	reconciler.TokenClient = &githubapp.TokenClient{BaseURL: server.URL, Client: server.Client()}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("Reconcile() requeueAfter = %s, want positive refresh interval", result.RequeueAfter)
	}
	var recreated corev1.Secret
	if err := cl.Get(context.Background(), client.ObjectKey{Namespace: session.Namespace, Name: sessionGitHubTokenSecretName(session.Name)}, &recreated); err != nil {
		t.Fatalf("getting recreated token Secret: %v", err)
	}
	if got := string(recreated.Data[GitHubTokenSecretKey]); got != "ghs_recreated" {
		t.Fatalf("recreated token = %q, want ghs_recreated", got)
	}
	if !metav1.IsControlledBy(&recreated, session) {
		t.Fatal("recreated token Secret is not controlled by the Session")
	}
}

func TestSessionPhaseForPodReportsContainerFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status corev1.PodStatus
		reason string
	}{
		{
			name: "runtime crash loop",
			status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "agent",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off restarting failed container"}},
			}}},
			reason: "CrashLoopBackOff",
		},
		{
			name: "runtime image pull failure",
			status: corev1.PodStatus{InitContainerStatuses: []corev1.ContainerStatus{{
				Name:  "kelos-session-runtime",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
			}}},
			reason: "ImagePullBackOff",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			phase, message, reason := sessionPhaseForPod(&corev1.Pod{Status: tt.status})
			if phase != kelos.SessionPhaseFailed || reason != tt.reason {
				t.Fatalf("sessionPhaseForPod() = (%q, %q, %q), want Failed with reason %q", phase, message, reason, tt.reason)
			}
			if !strings.Contains(message, tt.reason) {
				t.Fatalf("failure message %q does not contain %q", message, tt.reason)
			}
		})
	}
}

func TestSessionReconcilerDoesNotReplaceLostPod(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kelos.AddToScheme(scheme)
	session := testSession("lost", "codex")
	session.Status = kelos.SessionStatus{Phase: kelos.SessionPhaseReady, PodName: "lost", PodUID: types.UID("pod-1")}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kelos.Session{}).WithObjects(session).Build()
	reconciler := testSessionReconciler(cl, scheme)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.SessionPhaseFailed || updated.Status.Message != "Session Pod was lost" {
		t.Fatalf("Session status = %#v", updated.Status)
	}
	var pods corev1.PodList
	if err := cl.List(context.Background(), &pods, client.InNamespace(session.Namespace)); err != nil {
		t.Fatal(err)
	}
	if len(pods.Items) != 0 {
		t.Fatalf("created %d replacement Pods, want none", len(pods.Items))
	}
}

func TestSessionReconcilerWaitsForWorkspace(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = kelos.AddToScheme(scheme)
	session := testSession("waiting", "claude-code")
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "missing"}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&kelos.Session{}).WithObjects(session).Build()
	reconciler := testSessionReconciler(cl, scheme)
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(session)})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("Reconcile() did not requeue for missing Workspace")
	}
	var updated kelos.Session
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(session), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != kelos.SessionPhasePending || updated.Status.Message != "Waiting for Workspace \"missing\"" {
		t.Fatalf("Session status = %#v", updated.Status)
	}
}

func testSession(name, provider string) *kelos.Session {
	return &kelos.Session{
		TypeMeta: metav1.TypeMeta{APIVersion: kelos.GroupVersion.String(), Kind: "Session"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       types.UID(name + "-uid"),
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type: provider,
			Credentials: &kelos.Credentials{
				Type: kelos.CredentialTypeNone,
			},
		}},
	}
}

func testSessionReconciler(cl client.Client, scheme *runtime.Scheme) *SessionReconciler {
	builder := NewJobBuilder()
	builder.ClaudeCodeImage = "claude:test"
	builder.CodexImage = "codex:test"
	builder.OpenCodeImage = "opencode:test"
	return &SessionReconciler{
		Client:              cl,
		Scheme:              scheme,
		JobBuilder:          builder,
		SessionRuntimeImage: "runtime:test",
		Recorder:            record.NewFakeRecorder(10),
	}
}
