package webhook

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/githubapp"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

// gatewayWebhookSecretKey is the Secret data key holding the HMAC secret used to
// verify inbound deliveries for a github/linear WebhookGateway.
const gatewayWebhookSecretKey = "webhook-secret"

// gatewayMaxPayloadSize bounds the request body the gateway reads, matching the
// legacy per-source handler. GitHub caps webhook payloads at 25 MB.
const gatewayMaxPayloadSize = 10 * 1024 * 1024

// GatewayHandler serves webhook deliveries addressed to a per-gateway path
// (/webhook/<namespace>/<name>). It resolves the WebhookGateway named by the
// path, verifies the delivery against that gateway's secret (github/linear),
// then fans out only to TaskSpawners in the gateway's namespace that reference
// it via gatewayRef. The task builder and delivery cache are shared across
// requests; a per-request WebhookHandler carries the resolved source, secret,
// token resolver, and API base URL.
type GatewayHandler struct {
	client        client.Client
	log           logr.Logger
	taskBuilder   *taskbuilder.TaskBuilder
	deliveryCache *DeliveryCache
}

// NewGatewayHandler creates a GatewayHandler with a shared task builder and
// delivery cache.
func NewGatewayHandler(ctx context.Context, cl client.Client, log logr.Logger) (*GatewayHandler, error) {
	taskBuilder, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("failed to create task builder: %w", err)
	}
	return &GatewayHandler{
		client:        cl,
		log:           log,
		taskBuilder:   taskBuilder,
		deliveryCache: NewDeliveryCache(ctx),
	}, nil
}

// parseGatewayPath extracts the namespace and name from a gateway webhook path
// of the form /webhook/<namespace>/<name>.
func parseGatewayPath(path string) (namespace, name string, err error) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != 3 || segments[0] != "webhook" || segments[1] == "" || segments[2] == "" {
		return "", "", fmt.Errorf("invalid gateway webhook path %q: expected /webhook/<namespace>/<name>", path)
	}
	return segments[1], segments[2], nil
}

// ServeHTTP handles a webhook delivery for a per-gateway path.
func (g *GatewayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := g.log.WithValues("method", r.Method, "path", r.URL.Path, "remoteAddr", r.RemoteAddr)

	if r.Method != http.MethodPost {
		log.Info("Rejected non-POST request", "method", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	namespace, name, err := parseGatewayPath(r.URL.Path)
	if err != nil {
		log.Info("Invalid gateway webhook path", "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log = log.WithValues("gatewayNamespace", namespace, "gatewayName", name)

	var gateway v1alpha1.WebhookGateway
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &gateway); err != nil {
		log.Info("WebhookGateway not found", "error", err)
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, gatewayMaxPayloadSize+1))
	if err != nil {
		log.Error(err, "Failed to read request body")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	if len(body) > gatewayMaxPayloadSize {
		log.Info("Rejected oversized webhook payload", "size", len(body))
		http.Error(w, "Payload too large", http.StatusRequestEntityTooLarge)
		return
	}

	source, err := gatewaySourceForType(gateway.Spec.Type)
	if err != nil {
		log.Error(err, "Unsupported gateway type", "type", gateway.Spec.Type)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Resolve the inbound HMAC secret for github/linear. Generic gateways are
	// accepted without verification in this version.
	var secret []byte
	if source != GenericSource {
		secret, err = g.resolveGatewaySecret(ctx, &gateway)
		if err != nil {
			log.Error(err, "Failed to resolve gateway secret")
			http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
	}

	// Extract per-source headers, verify the signature, and derive a delivery ID.
	var eventType, deliveryID string
	var scopedSpawners []*v1alpha1.TaskSpawner

	switch source {
	case GitHubSource:
		eventType = r.Header.Get(GitHubEventHeader)
		deliveryID = r.Header.Get(GitHubDeliveryHeader)
		if err := ValidateGitHubSignature(body, r.Header.Get(GitHubSignatureHeader), secret); err != nil {
			log.Error(err, "GitHub signature validation failed", "eventType", eventType, "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case LinearSource:
		eventType = "linear"
		deliveryID = r.Header.Get(LinearDeliveryHeader)
		if deliveryID == "" {
			deliveryID = linearDeliveryID(body)
		}
		if err := ValidateLinearSignature(body, r.Header.Get(LinearSignatureHeader), secret); err != nil {
			log.Error(err, "Linear signature validation failed", "deliveryID", deliveryID)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

	case GenericSource:
		// No verification scheme is configured for generic gateways yet. Accept
		// the delivery but log loudly so the lack of authentication is visible
		// in server logs (the gateway's status also surfaces Unauthenticated).
		eventType = name
		scopedSpawners = g.listGatewayScopedSpawners(ctx, namespace, name, source)
		deliveryID = extractGenericDeliveryID(name, body, scopedSpawners)
		log.Info("WARNING: accepting generic webhook without signature verification", "deliveryID", deliveryID)
	}

	if deliveryID != "" && g.deliveryCache.CheckAndMark(deliveryID) {
		log.Info("Duplicate webhook delivery, returning cached response", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	if scopedSpawners == nil {
		scopedSpawners = g.listGatewayScopedSpawners(ctx, namespace, name, source)
	}
	if len(scopedSpawners) == 0 {
		log.Info("No TaskSpawners reference this gateway", "eventType", eventType, "deliveryID", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Build a per-request token resolver from the gateway's credentialsRef so
	// outbound GitHub API calls (enrichment, reporting) use per-instance creds.
	tokenResolver, err := g.resolveGatewayTokenResolver(ctx, &gateway)
	if err != nil {
		log.Error(err, "Failed to resolve gateway credentials")
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	wh := g.handlerForGateway(&gateway, source, secret, tokenResolver)
	if _, err := wh.processWebhook(ctx, eventType, body, deliveryID, scopedSpawners); err != nil {
		log.Error(err, "Failed to process webhook", "eventType", eventType, "deliveryID", deliveryID)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	log.Info("Webhook processed successfully", "eventType", eventType, "deliveryID", deliveryID)
	w.WriteHeader(http.StatusOK)
}

// handlerForGateway builds a per-request WebhookHandler that shares the gateway
// handler's task builder and delivery cache.
func (g *GatewayHandler) handlerForGateway(gw *v1alpha1.WebhookGateway, source WebhookSource, secret []byte, tokenResolver func(context.Context) (string, error)) *WebhookHandler {
	return &WebhookHandler{
		client:           g.client,
		source:           source,
		log:              g.log.WithValues("gateway", gw.Name, "namespace", gw.Namespace),
		taskBuilder:      g.taskBuilder,
		secret:           secret,
		deliveryCache:    g.deliveryCache,
		githubAPIBaseURL: gw.Spec.APIBaseURL,
		tokenResolver:    tokenResolver,
		gatewayName:      gw.Name,
	}
}

// listGatewayScopedSpawners returns TaskSpawners in the gateway's namespace
// whose matching webhook block references this gateway by name.
func (g *GatewayHandler) listGatewayScopedSpawners(ctx context.Context, namespace, name string, source WebhookSource) []*v1alpha1.TaskSpawner {
	var spawnerList v1alpha1.TaskSpawnerList
	if err := g.client.List(ctx, &spawnerList, client.InNamespace(namespace)); err != nil {
		g.log.Error(err, "Failed to list TaskSpawners", "namespace", namespace)
		return nil
	}

	var spawners []*v1alpha1.TaskSpawner
	for i := range spawnerList.Items {
		when := &spawnerList.Items[i].Spec.When
		var ref *v1alpha1.GatewayReference
		switch source {
		case GitHubSource:
			if when.GitHubWebhook != nil {
				ref = when.GitHubWebhook.GatewayRef
			}
		case LinearSource:
			if when.LinearWebhook != nil {
				ref = when.LinearWebhook.GatewayRef
			}
		case GenericSource:
			if when.GenericWebhook != nil {
				ref = when.GenericWebhook.GatewayRef
			}
		}
		if ref != nil && ref.Name == name {
			spawners = append(spawners, &spawnerList.Items[i])
		}
	}
	return spawners
}

// resolveGatewaySecret reads the HMAC secret for a github/linear gateway.
func (g *GatewayHandler) resolveGatewaySecret(ctx context.Context, gw *v1alpha1.WebhookGateway) ([]byte, error) {
	if gw.Spec.SecretRef == nil {
		return nil, fmt.Errorf("gateway %s/%s has no secretRef", gw.Namespace, gw.Name)
	}
	var secret corev1.Secret
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: gw.Spec.SecretRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("fetching gateway secret %s: %w", gw.Spec.SecretRef.Name, err)
	}
	value := secret.Data[gatewayWebhookSecretKey]
	if len(value) == 0 {
		return nil, fmt.Errorf("gateway secret %s is missing key %q", gw.Spec.SecretRef.Name, gatewayWebhookSecretKey)
	}
	return value, nil
}

// resolveGatewayTokenResolver builds a GitHub token resolver from the gateway's
// credentialsRef. Returns nil when no credentialsRef is configured.
func (g *GatewayHandler) resolveGatewayTokenResolver(ctx context.Context, gw *v1alpha1.WebhookGateway) (func(context.Context) (string, error), error) {
	if gw.Spec.CredentialsRef == nil {
		return nil, nil
	}
	var secret corev1.Secret
	if err := g.client.Get(ctx, types.NamespacedName{Namespace: gw.Namespace, Name: gw.Spec.CredentialsRef.Name}, &secret); err != nil {
		return nil, fmt.Errorf("fetching gateway credentials %s: %w", gw.Spec.CredentialsRef.Name, err)
	}
	return githubapp.NewSecretTokenResolver(secret.Data, gw.Spec.APIBaseURL)
}

// gatewaySourceForType maps a WebhookGateway type to the internal WebhookSource.
func gatewaySourceForType(t v1alpha1.WebhookGatewayType) (WebhookSource, error) {
	switch t {
	case v1alpha1.WebhookGatewayTypeGitHub:
		return GitHubSource, nil
	case v1alpha1.WebhookGatewayTypeLinear:
		return LinearSource, nil
	case v1alpha1.WebhookGatewayTypeGeneric:
		return GenericSource, nil
	default:
		return "", fmt.Errorf("unsupported gateway type %q", t)
	}
}
