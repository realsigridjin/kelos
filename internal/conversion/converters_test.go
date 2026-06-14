package conversion

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	v1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	v1alpha2 "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestWebhookRegistrationsBuildConverters(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("Add v1alpha1 to scheme: %v", err)
	}
	if err := v1alpha2.AddToScheme(scheme); err != nil {
		t.Fatalf("Add v1alpha2 to scheme: %v", err)
	}

	registrations := WebhookRegistrations()
	if len(registrations) == 0 {
		t.Fatal("WebhookRegistrations returned no registrations")
	}
	for _, registration := range registrations {
		converter, err := registration.Converter(scheme)
		if err != nil {
			t.Fatalf("build converter for %T: %v", registration.Object, err)
		}
		if converter == nil {
			t.Fatalf("converter for %T is nil", registration.Object)
		}
	}
}
