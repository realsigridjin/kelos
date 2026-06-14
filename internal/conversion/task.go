package conversion

import (
	"context"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func taskToHub(_ context.Context, src *v1alpha1.Task, dst *v1alpha2.Task) error {
	dst.ObjectMeta = src.ObjectMeta
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	foldTaskAgentConfigRefForward(&src.Spec, &dst.Spec)
	return convertViaJSON(&src.Status, &dst.Status)
}

func taskFromHub(_ context.Context, src *v1alpha2.Task, dst *v1alpha1.Task) error {
	dst.ObjectMeta = src.ObjectMeta
	if err := convertViaJSON(&src.Spec, &dst.Spec); err != nil {
		return err
	}
	return convertViaJSON(&src.Status, &dst.Status)
}

func foldTaskAgentConfigRefForward(src *v1alpha1.TaskSpec, dst *v1alpha2.TaskSpec) {
	if len(dst.AgentConfigRefs) == 0 && src.AgentConfigRef != nil {
		dst.AgentConfigRefs = []v1alpha2.AgentConfigReference{{Name: src.AgentConfigRef.Name}}
	}
}
