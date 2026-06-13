package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

var scheme = runtime.NewScheme()

const (
	kelosDiscoveryAttempts = 20
	kelosDiscoveryInterval = 100 * time.Millisecond
)

type kelosAPIResource struct {
	resource string
	kind     string
}

type serverResourceDiscovery interface {
	ServerResourcesForGroupVersion(groupVersion string) (*metav1.APIResourceList, error)
}

var requiredKelosAPIResources = []kelosAPIResource{
	{resource: "agentconfigs", kind: "AgentConfig"},
	{resource: "tasks", kind: "Task"},
	{resource: "taskspawners", kind: "TaskSpawner"},
	{resource: "workspaces", kind: "Workspace"},
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
}

// ClientConfig holds configuration for Kubernetes client creation.
type ClientConfig struct {
	Kubeconfig string
	Namespace  string
	Config     *Config
}

// NewClient creates a controller-runtime client and resolves the namespace.
func (c *ClientConfig) NewClient() (client.Client, string, error) {
	restConfig, ns, err := c.resolveConfig()
	if err != nil {
		return nil, "", err
	}
	if err := ensureKelosAPIResources(restConfig); err != nil {
		return nil, "", err
	}
	cl, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("creating client: %w", err)
	}
	return cl, ns, nil
}

// NewClientset creates a kubernetes.Clientset and resolves the namespace.
func (c *ClientConfig) NewClientset() (*kubernetes.Clientset, string, error) {
	restConfig, ns, err := c.resolveConfig()
	if err != nil {
		return nil, "", err
	}
	cs, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, "", fmt.Errorf("creating clientset: %w", err)
	}
	return cs, ns, nil
}

func (c *ClientConfig) resolveConfig() (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.Kubeconfig != "" {
		rules.ExplicitPath = c.Kubeconfig
	}
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})

	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading kubeconfig: %w", err)
	}

	ns := c.Namespace
	if ns == "" {
		ns, _, err = config.Namespace()
		if err != nil {
			return nil, "", fmt.Errorf("resolving namespace: %w", err)
		}
		if ns == "" {
			ns = "default"
		}
	}

	return restConfig, ns, nil
}

func ensureKelosAPIResources(restConfig *rest.Config) error {
	dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating discovery client: %w", err)
	}
	return waitForKelosAPIResources(dc, kelosDiscoveryAttempts, kelosDiscoveryInterval)
}

func waitForKelosAPIResources(dc serverResourceDiscovery, attempts int, interval time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := kelosAPIResourcesReady(dc)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isMissingKelosAPIResources(err) {
			return fmt.Errorf("discovering Kelos API resources: %w", err)
		}
		if attempt < attempts-1 && interval > 0 {
			time.Sleep(interval)
		}
	}

	return fmt.Errorf("discovering Kelos API resources: %w", lastErr)
}

func kelosAPIResourcesReady(dc serverResourceDiscovery) error {
	groupVersion := kelosv1alpha1.GroupVersion.String()
	resources, err := dc.ServerResourcesForGroupVersion(groupVersion)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &missingKelosAPIResourcesError{
				groupVersion: groupVersion,
				missing:      requiredKelosAPIResources,
				err:          err,
			}
		}
		return err
	}

	seen := map[string]string{}
	if resources != nil {
		for _, resource := range resources.APIResources {
			seen[resource.Name] = resource.Kind
		}
	}

	var missing []kelosAPIResource
	for _, expected := range requiredKelosAPIResources {
		if kind, ok := seen[expected.resource]; !ok || kind != expected.kind {
			missing = append(missing, expected)
		}
	}
	if len(missing) > 0 {
		return &missingKelosAPIResourcesError{
			groupVersion: groupVersion,
			missing:      missing,
		}
	}
	return nil
}

type missingKelosAPIResourcesError struct {
	groupVersion string
	missing      []kelosAPIResource
	err          error
}

func (e *missingKelosAPIResourcesError) Error() string {
	names := make([]string, 0, len(e.missing))
	for _, resource := range e.missing {
		names = append(names, fmt.Sprintf("%s/%s", resource.resource, resource.kind))
	}
	msg := fmt.Sprintf("%s API discovery is missing %s", e.groupVersion, strings.Join(names, ", "))
	if e.err != nil {
		msg = fmt.Sprintf("%s: %v", msg, e.err)
	}
	return msg
}

func (e *missingKelosAPIResourcesError) Unwrap() error {
	return e.err
}

func isMissingKelosAPIResources(err error) bool {
	var missing *missingKelosAPIResourcesError
	return errors.As(err, &missing)
}

// newClientOrDryRun returns a real client and namespace when dryRun is
// false, or a nil client with a resolved namespace when dryRun is true.
func newClientOrDryRun(cfg *ClientConfig, dryRun bool) (client.Client, string, error) {
	if dryRun {
		return nil, cfg.ResolveNamespace(), nil
	}
	return cfg.NewClient()
}

// ResolveNamespace returns the namespace to use, resolving from the
// kubeconfig context when no explicit namespace is set. It falls back
// to "default" when the namespace cannot be determined.
func (c *ClientConfig) ResolveNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}

	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if c.Kubeconfig != "" {
		rules.ExplicitPath = c.Kubeconfig
	}
	config := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	ns, _, err := config.Namespace()
	if err != nil || ns == "" {
		return "default"
	}
	return ns
}
