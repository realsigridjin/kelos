package conversion

import (
	"context"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func workspaceToHub(_ context.Context, src *v1alpha1.Workspace, dst *v1alpha2.Workspace) error {
	dst.ObjectMeta = src.ObjectMeta
	return convertViaJSON(&src.Spec, &dst.Spec)
}

func workspaceFromHub(_ context.Context, src *v1alpha2.Workspace, dst *v1alpha1.Workspace) error {
	dst.ObjectMeta = src.ObjectMeta
	return convertViaJSON(&src.Spec, &dst.Spec)
}
