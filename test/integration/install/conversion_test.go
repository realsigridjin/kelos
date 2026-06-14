package install

import (
	"k8s.io/client-go/kubernetes/scheme"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func init() {
	if err := kelosv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
}
