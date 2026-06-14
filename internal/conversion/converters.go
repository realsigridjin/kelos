package conversion

import (
	"k8s.io/apimachinery/pkg/runtime"
	webhookconversion "sigs.k8s.io/controller-runtime/pkg/webhook/conversion"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

// WebhookRegistration describes one CRD kind served by the shared conversion
// webhook.
type WebhookRegistration struct {
	Object    runtime.Object
	Converter func(*runtime.Scheme) (webhookconversion.Converter, error)
}

func WebhookRegistrations() []WebhookRegistration {
	return []WebhookRegistration{
		{Object: &v1alpha2.AgentConfig{}, Converter: AgentConfigConverter()},
		{Object: &v1alpha2.Task{}, Converter: TaskConverter()},
		{Object: &v1alpha2.Workspace{}, Converter: WorkspaceConverter()},
		{Object: &v1alpha2.TaskSpawner{}, Converter: TaskSpawnerConverter()},
	}
}

func AgentConfigConverter() func(*runtime.Scheme) (webhookconversion.Converter, error) {
	return webhookconversion.NewHubSpokeConverter(&v1alpha2.AgentConfig{},
		webhookconversion.NewSpokeConverter(&v1alpha1.AgentConfig{}, agentConfigFromHub, agentConfigToHub),
	)
}

func TaskConverter() func(*runtime.Scheme) (webhookconversion.Converter, error) {
	return webhookconversion.NewHubSpokeConverter(&v1alpha2.Task{},
		webhookconversion.NewSpokeConverter(&v1alpha1.Task{}, taskFromHub, taskToHub),
	)
}

func WorkspaceConverter() func(*runtime.Scheme) (webhookconversion.Converter, error) {
	return webhookconversion.NewHubSpokeConverter(&v1alpha2.Workspace{},
		webhookconversion.NewSpokeConverter(&v1alpha1.Workspace{}, workspaceFromHub, workspaceToHub),
	)
}

func TaskSpawnerConverter() func(*runtime.Scheme) (webhookconversion.Converter, error) {
	return webhookconversion.NewHubSpokeConverter(&v1alpha2.TaskSpawner{},
		webhookconversion.NewSpokeConverter(&v1alpha1.TaskSpawner{}, taskSpawnerFromHub, taskSpawnerToHub),
	)
}
