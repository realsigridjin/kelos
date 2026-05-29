package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WebhookGatewayType identifies the webhook source a WebhookGateway serves.
// It selects the signature scheme used to verify inbound deliveries.
type WebhookGatewayType string

const (
	// WebhookGatewayTypeGitHub serves GitHub webhook deliveries.
	WebhookGatewayTypeGitHub WebhookGatewayType = "github"
	// WebhookGatewayTypeLinear serves Linear webhook deliveries.
	WebhookGatewayTypeLinear WebhookGatewayType = "linear"
	// WebhookGatewayTypeGeneric serves arbitrary HTTP POST deliveries.
	WebhookGatewayTypeGeneric WebhookGatewayType = "generic"
)

// WebhookGatewayPhase represents the authentication state of a WebhookGateway.
type WebhookGatewayPhase string

const (
	// WebhookGatewayPhaseAuthenticated means inbound deliveries are HMAC-verified
	// against the gateway's secret.
	WebhookGatewayPhaseAuthenticated WebhookGatewayPhase = "Authenticated"
	// WebhookGatewayPhaseSecretMissing means a required Secret (the HMAC secret
	// or, for github, the API credentials) is not configured or not yet present.
	WebhookGatewayPhaseSecretMissing WebhookGatewayPhase = "SecretMissing"
	// WebhookGatewayPhaseUnauthenticated means inbound deliveries are accepted
	// without verification. Generic gateways are always unauthenticated in this
	// version because no per-provider signature scheme is configured.
	WebhookGatewayPhaseUnauthenticated WebhookGatewayPhase = "Unauthenticated"
)

// WebhookGatewaySpec defines the desired state of a WebhookGateway.
// +kubebuilder:validation:XValidation:rule="self.type == 'generic' || has(self.secretRef)",message="secretRef is required for github and linear gateways"
// +kubebuilder:validation:XValidation:rule="(!has(self.apiBaseURL) && !has(self.credentialsRef)) || self.type == 'github'",message="apiBaseURL and credentialsRef are only valid for github gateways"
type WebhookGatewaySpec struct {
	// Type identifies the webhook source this gateway serves. It selects the
	// signature scheme used to verify inbound deliveries.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=github;linear;generic
	Type WebhookGatewayType `json:"type"`

	// SecretRef references a Secret holding the HMAC secret used to verify
	// inbound deliveries. Required for github and linear gateways. Unused for
	// generic gateways, which are accepted without verification in this version.
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`

	// APIBaseURL is the GitHub API base URL used for outbound API calls (pull
	// request file enrichment and status reporting), e.g.
	// "https://ghe.example.com/api/v3" for a GitHub Enterprise instance. When
	// empty, "https://api.github.com" is used. Only valid for github gateways.
	// +optional
	APIBaseURL string `json:"apiBaseURL,omitempty"`

	// CredentialsRef references a Secret holding GitHub API credentials (a
	// personal access token or GitHub App credentials) used for outbound API
	// calls. Only valid for github gateways.
	// +optional
	CredentialsRef *SecretReference `json:"credentialsRef,omitempty"`
}

// WebhookGatewayStatus defines the observed state of a WebhookGateway.
type WebhookGatewayStatus struct {
	// URL is the inbound path this gateway listens on, derived as
	// /webhook/<namespace>/<name>. It is relative to the externally configured
	// webhook host.
	// +optional
	URL string `json:"url,omitempty"`

	// Phase summarizes the gateway's authentication state.
	// +optional
	Phase WebhookGatewayPhase `json:"phase,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WebhookGateway is the Schema for the webhookgateways API. It is a per-channel
// authentication and routing boundary for webhook-driven TaskSpawners: it owns
// one inbound path (/webhook/<namespace>/<name>) and, for github/linear, the
// secret used to verify deliveries, then fans out to TaskSpawners in its own
// namespace that reference it via gatewayRef.
type WebhookGateway struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WebhookGatewaySpec   `json:"spec,omitempty"`
	Status WebhookGatewayStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WebhookGatewayList contains a list of WebhookGateway.
type WebhookGatewayList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WebhookGateway `json:"items"`
}

// GatewayReference refers to a WebhookGateway resource by name in the same
// namespace as the referencing TaskSpawner.
type GatewayReference struct {
	// Name is the name of the WebhookGateway resource.
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&WebhookGateway{}, &WebhookGatewayList{})
}
