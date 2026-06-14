package cli

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestListKelosResource_FallsBackToV1alpha1(t *testing.T) {
	scheme := runtime.NewScheme()
	// Both list kinds must be registered or the fake client panics before any
	// reactor runs; the reactor below makes v1alpha2 behave as if unserved.
	listKinds := map[schema.GroupVersionResource]string{
		{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"}: "TaskList",
		{Group: "kelos.dev", Version: "v1alpha2", Resource: "tasks"}: "TaskList",
	}
	task := &unstructured.Unstructured{}
	task.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha1", Kind: "Task"})
	task.SetName("legacy-task")
	task.SetNamespace("default")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, task)
	// Simulate a cluster whose CRDs predate v1alpha2: listing the v1alpha2
	// version returns NoMatch, so listKelosResource must fall back to v1alpha1.
	client.PrependReactor("list", "tasks", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetResource().Version == "v1alpha2" {
			return true, nil, &meta.NoResourceMatchError{PartialResource: action.GetResource()}
		}
		return false, nil, nil
	})

	gvr, list, ok, err := listKelosResource(context.Background(), client, "tasks", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true when only v1alpha1 is served")
	}
	if gvr.Version != "v1alpha1" {
		t.Errorf("resolved version = %q, want v1alpha1", gvr.Version)
	}
	if list == nil || len(list.Items) != 1 {
		t.Fatalf("expected 1 item listed via v1alpha1, got %v", list)
	}

	taskGVR := schema.GroupVersionResource{Group: "kelos.dev", Version: "v1alpha1", Resource: "tasks"}
	got, err := client.Resource(taskGVR).Namespace("default").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing fallback resource: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("fallback resource count = %d, want 1", len(got.Items))
	}
}
