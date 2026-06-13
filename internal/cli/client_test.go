package cli

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestWaitForKelosAPIResourcesRetriesMissingResources(t *testing.T) {
	dc := &fakeServerResourceDiscovery{
		responses: []*metav1.APIResourceList{
			kelosAPIResourceListWithout("taskspawners"),
			completeKelosAPIResourceList(),
		},
	}

	if err := waitForKelosAPIResources(dc, 2, 0); err != nil {
		t.Fatalf("waitForKelosAPIResources returned error: %v", err)
	}
	if dc.calls != 2 {
		t.Fatalf("expected 2 discovery calls, got %d", dc.calls)
	}
}

func TestWaitForKelosAPIResourcesFailsWithMissingResourceName(t *testing.T) {
	dc := &fakeServerResourceDiscovery{
		responses: []*metav1.APIResourceList{
			kelosAPIResourceListWithout("taskspawners"),
		},
	}

	err := waitForKelosAPIResources(dc, 1, 0)
	if err == nil {
		t.Fatal("expected error for missing TaskSpawner discovery")
	}
	if !strings.Contains(err.Error(), "taskspawners/TaskSpawner") {
		t.Fatalf("expected error to name missing TaskSpawner resource, got %v", err)
	}
}

func TestWaitForKelosAPIResourcesDoesNotRetryUnexpectedDiscoveryError(t *testing.T) {
	discoveryErr := errors.New("transport failed")
	dc := &fakeServerResourceDiscovery{
		errors: []error{discoveryErr},
	}

	err := waitForKelosAPIResources(dc, 3, 0)
	if !errors.Is(err, discoveryErr) {
		t.Fatalf("expected wrapped discovery error, got %v", err)
	}
	if dc.calls != 1 {
		t.Fatalf("expected one discovery call, got %d", dc.calls)
	}
}

type fakeServerResourceDiscovery struct {
	responses []*metav1.APIResourceList
	errors    []error
	calls     int
}

func (f *fakeServerResourceDiscovery) ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error) {
	if groupVersion != kelosv1alpha1.GroupVersion.String() {
		return nil, errors.New("unexpected group version")
	}

	f.calls++
	index := f.calls - 1
	if index < len(f.errors) && f.errors[index] != nil {
		return nil, f.errors[index]
	}
	if index < len(f.responses) {
		return f.responses[index], nil
	}
	if len(f.responses) == 0 {
		return &metav1.APIResourceList{GroupVersion: groupVersion}, nil
	}
	return f.responses[len(f.responses)-1], nil
}

func completeKelosAPIResourceList() *metav1.APIResourceList {
	resources := make([]metav1.APIResource, 0, len(requiredKelosAPIResources))
	for _, resource := range requiredKelosAPIResources {
		resources = append(resources, metav1.APIResource{
			Name:       resource.resource,
			Kind:       resource.kind,
			Namespaced: true,
		})
	}
	return &metav1.APIResourceList{
		GroupVersion: kelosv1alpha1.GroupVersion.String(),
		APIResources: resources,
	}
}

func kelosAPIResourceListWithout(resourceName string) *metav1.APIResourceList {
	resources := completeKelosAPIResourceList()
	filtered := resources.APIResources[:0]
	for _, resource := range resources.APIResources {
		if resource.Name != resourceName {
			filtered = append(filtered, resource)
		}
	}
	resources.APIResources = filtered
	return resources
}
