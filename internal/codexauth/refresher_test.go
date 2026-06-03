package codexauth

import (
	"context"
	"encoding/json"
	"testing"

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

	if seeded["last_refresh"] != staleRefresh {
		t.Fatalf("seeded last_refresh = %v, want %s", seeded["last_refresh"], staleRefresh)
	}
	updated, err := clientset.CoreV1().Secrets("default").Get(ctx, "codex", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting Secret: %v", err)
	}
	if got := string(updated.Data[secretKey]); got != `{"tokens":{"access_token":"new","id_token":"id","refresh_token":"refresh"},"last_refresh":"2026-06-02T00:00:00Z"}` {
		t.Fatalf("updated auth = %s", got)
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

func TestVerifyRefreshedRequiresChangedLastRefresh(t *testing.T) {
	if err := verifyRefreshed([]byte(`{"last_refresh":"1970-01-01T00:00:00Z"}`)); err == nil {
		t.Fatal("verifyRefreshed() succeeded for stale last_refresh")
	}
	if err := verifyRefreshed([]byte(`{"last_refresh":"2026-06-02T00:00:00Z"}`)); err != nil {
		t.Fatalf("verifyRefreshed() error = %v", err)
	}
}
