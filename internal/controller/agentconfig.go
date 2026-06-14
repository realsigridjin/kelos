package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func (r *TaskReconciler) getAgentConfig(ctx context.Context, key client.ObjectKey) (*kelos.AgentConfig, error) {
	ac := &kelos.AgentConfig{}
	if err := r.Get(ctx, key, ac); err != nil {
		return nil, err
	}
	return ac, nil
}
