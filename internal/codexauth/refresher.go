// Package codexauth refreshes Codex OAuth credentials stored in Kubernetes
// Secrets by calling the OpenAI OAuth token endpoint with the refresh token.
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// RefreshLabel is the Secret label that opts a credentials Secret into
	// scheduled Codex OAuth refresh.
	RefreshLabel = "kelos.dev/codex-oauth-refresh"

	secretKey               = "CODEX_AUTH_JSON"
	oauthTokenURL           = "https://auth.openai.com/oauth/token"
	oauthTokenMethod        = http.MethodPost
	oauthGrantType          = "refresh_token"
	oauthClientIDKey        = "client_id"
	oauthContentType        = "application/x-www-form-urlencoded"
	defaultOAuthClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	defaultOAuthHTTPTimeout = 30 * time.Second
)

// Runner refreshes auth.json bytes and returns the bytes that should be stored.
type Runner func(context.Context, []byte) ([]byte, error)

// Options configures a refresh run.
type Options struct {
	// Namespace is the namespace of the target Secret.
	Namespace string
	// SecretName is the name of the target Secret.
	SecretName string
	// Runner refreshes a single auth.json bundle. Defaults to DefaultRunner.
	Runner Runner
}

func (o *Options) applyDefaults() {
	if o.Runner == nil {
		o.Runner = DefaultRunner
	}
}

// Run refreshes one opted-in Codex OAuth credentials Secret. Secrets without
// CODEX_AUTH_JSON, without the opt-in label, or whose bundle carries no
// refresh_token are skipped.
func Run(ctx context.Context, clientset kubernetes.Interface, opts Options) error {
	opts.applyDefaults()
	if clientset == nil {
		return errors.New("clientset is required")
	}
	if opts.Namespace == "" {
		return errors.New("namespace is required")
	}
	if opts.SecretName == "" {
		return errors.New("secret name is required")
	}

	secret, err := clientset.CoreV1().Secrets(opts.Namespace).Get(ctx, opts.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting Codex OAuth Secret %s/%s: %w", opts.Namespace, opts.SecretName, err)
	}
	if secret.Labels[RefreshLabel] != "true" {
		log("Skipping Codex OAuth Secret without refresh label", "namespace", secret.Namespace, "name", secret.Name)
		return nil
	}

	changed, err := refreshSecret(ctx, clientset, secret, opts.Runner)
	if err != nil {
		return fmt.Errorf("secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	if !changed {
		log("Skipped Codex OAuth Secret", "namespace", secret.Namespace, "name", secret.Name)
	}
	return nil
}

func refreshSecret(ctx context.Context, clientset kubernetes.Interface, s *corev1.Secret, runner Runner) (bool, error) {
	raw, ok := s.Data[secretKey]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}

	refreshable, ok, err := authWithRefreshToken(raw)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	updated, err := runner(ctx, refreshable)
	if err != nil {
		return false, err
	}
	if bytes.Equal(bytes.TrimSpace(updated), bytes.TrimSpace(raw)) {
		return false, nil
	}

	current, err := clientset.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("secret no longer exists: %w", err)
		}
		return false, fmt.Errorf("getting Secret: %w", err)
	}
	if current.Data == nil {
		current.Data = map[string][]byte{}
	}
	current.Data[secretKey] = updated
	if _, err := clientset.CoreV1().Secrets(s.Namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("secret no longer exists: %w", err)
		}
		return false, fmt.Errorf("updating Secret: %w", err)
	}

	log("Refreshed Codex OAuth credential", "namespace", s.Namespace, "name", s.Name)
	return true, nil
}

func authWithRefreshToken(raw []byte) ([]byte, bool, error) {
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, false, fmt.Errorf("parsing auth.json bundle: %w", err)
	}
	tokens, ok := bundle["tokens"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		return nil, false, nil
	}
	return raw, true, nil
}

// DefaultRunner refreshes auth.json by refreshing the OAuth access token against
// the OpenAI token endpoint.
func DefaultRunner(ctx context.Context, authJSON []byte) ([]byte, error) {
	return refreshWithOAuthToken(ctx, authJSON, oauthTokenURL, &http.Client{Timeout: defaultOAuthHTTPTimeout})
}

func refreshWithOAuthToken(ctx context.Context, authJSON []byte, tokenEndpoint string, client *http.Client) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("http client is required")
	}

	var bundle map[string]any
	if err := json.Unmarshal(authJSON, &bundle); err != nil {
		return nil, fmt.Errorf("parsing auth.json bundle: %w", err)
	}

	tokens, ok := bundle["tokens"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("auth.json missing tokens object")
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		return nil, fmt.Errorf("auth.json missing refresh_token")
	}
	clientID, _ := bundle[oauthClientIDKey].(string)
	if clientID == "" {
		clientID, _ = tokens["client_id"].(string)
	}
	if clientID == "" {
		clientID = defaultOAuthClientID
	}

	form := url.Values{}
	form.Set("grant_type", oauthGrantType)
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)

	request, err := http.NewRequestWithContext(ctx, oauthTokenMethod, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building OAuth refresh request: %w", err)
	}
	request.Header.Set("Content-Type", oauthContentType)

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("calling OAuth refresh endpoint: %w", err)
	}
	responseBody, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("reading OAuth refresh response: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("closing OAuth refresh response: %w", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth refresh endpoint returned %s: %s", response.Status, strings.TrimSpace(string(responseBody)))
	}

	var tokenResponse map[string]any
	if err := json.Unmarshal(responseBody, &tokenResponse); err != nil {
		return nil, fmt.Errorf("parsing OAuth refresh response: %w", err)
	}

	accessToken, _ := tokenResponse["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("OAuth response is missing access_token")
	}

	expiresAt, hasExpiresAt := tokenResponse["expires_at"]
	_, hasExpiresIn := tokenResponse["expires_in"]
	if (!hasExpiresAt || expiresAt == nil) && hasExpiresIn {
		delete(tokens, "expires_at")
	}

	for _, key := range []string{"access_token", "id_token", "refresh_token", "token_type", "scope", "expires_at", "expires_in"} {
		value, ok := tokenResponse[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		tokens[key] = value
	}

	bundle["last_refresh"] = time.Now().UTC().Format(time.RFC3339)

	refreshed, err := json.Marshal(bundle)
	if err != nil {
		return nil, fmt.Errorf("building refreshed auth.json bundle: %w", err)
	}
	return refreshed, nil
}

func log(message string, fields ...any) {
	fmt.Fprint(os.Stderr, message)
	for i := 0; i+1 < len(fields); i += 2 {
		fmt.Fprintf(os.Stderr, " %s=%v", fields[i], fields[i+1])
	}
	fmt.Fprintln(os.Stderr)
}
