package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	clientv1alpha2 "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/typed/api/v1alpha2"
)

// WorkspaceStatusPublisher publishes workspace state for one Session Pod.
type WorkspaceStatusPublisher func(context.Context, WorkspaceStatus) error

type sessionStatusPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// NewWorkspaceStatusPublisher creates a publisher scoped to one Session Pod.
func NewWorkspaceStatusPublisher(client clientv1alpha2.SessionInterface, sessionName string, podUID types.UID) (WorkspaceStatusPublisher, error) {
	if client == nil || sessionName == "" || podUID == "" {
		return nil, fmt.Errorf("Session client, name, and Pod UID are required")
	}
	return func(ctx context.Context, status WorkspaceStatus) error {
		patch, err := json.Marshal([]sessionStatusPatchOperation{
			{Op: "test", Path: "/status/podUID", Value: string(podUID)},
			{Op: "test", Path: "/status/phase", Value: kelos.SessionPhaseReady},
			{Op: "add", Path: "/status/branch", Value: status.Branch},
			{Op: "add", Path: "/status/pullRequest", Value: status.PullRequest},
		})
		if err != nil {
			return fmt.Errorf("encoding Session workspace status patch: %w", err)
		}
		if _, err := client.Patch(ctx, sessionName, types.JSONPatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
			return fmt.Errorf("publishing Session %q workspace status: %w", sessionName, err)
		}
		return nil
	}, nil
}
