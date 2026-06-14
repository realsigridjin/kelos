package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestGetAgentConfigReadsV1alpha2(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelos.AddToScheme(scheme))

	config := &kelos.AgentConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "default"},
		Spec: kelos.AgentConfigSpec{
			AgentsMD: "current instructions",
			MCPServers: []kelos.MCPServerSpec{{
				Name: "server",
				Type: "stdio",
				Env:  []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}},
			}},
		},
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(config).
		Build()
	r := &TaskReconciler{Client: cl}

	got, err := r.getAgentConfig(ctx, client.ObjectKey{Name: "config", Namespace: "default"})
	if err != nil {
		t.Fatalf("getAgentConfig: %v", err)
	}
	if got.Spec.AgentsMD != "current instructions" {
		t.Errorf("AgentsMD = %q, want current instructions", got.Spec.AgentsMD)
	}
	wantEnv := []corev1.EnvVar{{Name: "TOKEN", Value: "literal"}}
	if len(got.Spec.MCPServers) != 1 || !envVarsEqual(got.Spec.MCPServers[0].Env, wantEnv) {
		t.Errorf("MCP env = %#v, want %#v", got.Spec.MCPServers, wantEnv)
	}
}

func envVarsEqual(got, want []corev1.EnvVar) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Name != want[i].Name || got[i].Value != want[i].Value {
			return false
		}
	}
	return true
}
