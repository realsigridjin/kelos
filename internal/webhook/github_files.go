package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	prFilesMaxPages = 10
)

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

type githubFile struct {
	Filename string `json:"filename"`
}

// fetchPRChangedFiles fetches the list of changed files for a pull request
// from the GitHub API. It resolves the GitHub token from the workspace's
// secretRef, falling back to the global token resolver when the workspace
// does not provide one.
func fetchPRChangedFiles(ctx context.Context, cl client.Client, spawner *kelos.TaskSpawner, apiBaseURL, owner, repo string, number int) ([]string, error) {
	token, err := resolveGitHubTokenFromWorkspace(ctx, cl, spawner, apiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("resolving GitHub token for PR files: %w", err)
	}

	if token == "" && githubTokenResolver != nil {
		token, err = githubTokenResolver(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving GitHub token for PR files: %w", err)
		}
	}

	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	pageURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100",
		apiBaseURL, owner, repo, number)

	httpClient := &http.Client{}

	var allFiles []githubFile
	var page int
	for page = 0; pageURL != "" && page < prFilesMaxPages; page++ {
		var files []githubFile
		nextURL, err := fetchGitHubFilesPage(ctx, httpClient, pageURL, token, &files)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files...)
		pageURL = nextURL
	}

	// A partial file list is not safe for include/exclude decisions.
	if pageURL != "" && page >= prFilesMaxPages {
		return nil, fmt.Errorf("PR #%d has more than %d pages of changed files; refusing to evaluate filters on incomplete data", number, prFilesMaxPages)
	}

	paths := make([]string, len(allFiles))
	for i, f := range allFiles {
		paths[i] = f.Filename
	}
	return paths, nil
}

// resolveGitHubTokenFromWorkspace resolves a GitHub token from the workspace's
// secretRef. It supports both PAT (GITHUB_TOKEN key) and GitHub App credentials
// (appID, installationID, privateKey keys). Returns an empty string if no
// workspace or secret is configured.
func resolveGitHubTokenFromWorkspace(ctx context.Context, cl client.Client, spawner *kelos.TaskSpawner, apiBaseURL string) (string, error) {
	wsRef := spawner.Spec.TaskTemplate.WorkspaceRef
	if wsRef == nil {
		return "", nil
	}

	var ws kelos.Workspace
	if err := cl.Get(ctx, types.NamespacedName{
		Name:      wsRef.Name,
		Namespace: spawner.Namespace,
	}, &ws); err != nil {
		return "", fmt.Errorf("fetching workspace %s: %w", wsRef.Name, err)
	}

	if ws.Spec.SecretRef == nil {
		return "", nil
	}

	var secret corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{
		Name:      ws.Spec.SecretRef.Name,
		Namespace: spawner.Namespace,
	}, &secret); err != nil {
		return "", fmt.Errorf("fetching secret %s: %w", ws.Spec.SecretRef.Name, err)
	}

	if pat := string(secret.Data["GITHUB_TOKEN"]); pat != "" {
		return pat, nil
	}

	if githubapp.IsGitHubApp(secret.Data) {
		creds, err := githubapp.ParseCredentials(secret.Data)
		if err != nil {
			return "", fmt.Errorf("parsing GitHub App credentials from secret %s: %w", ws.Spec.SecretRef.Name, err)
		}
		tc := githubapp.NewTokenClient()
		if apiBaseURL != "" {
			tc.BaseURL = apiBaseURL
		}
		tp := githubapp.NewTokenProvider(tc, creds)
		token, err := tp.Token(ctx)
		if err != nil {
			return "", fmt.Errorf("generating GitHub App token from secret %s: %w", ws.Spec.SecretRef.Name, err)
		}
		return token, nil
	}

	return "", fmt.Errorf("secret %s referenced by workspace %s contains neither a GITHUB_TOKEN key nor valid GitHub App credentials (appID, installationID, privateKey)", ws.Spec.SecretRef.Name, wsRef.Name)
}

func fetchGitHubFilesPage(ctx context.Context, httpClient *http.Client, pageURL, token string, out *[]githubFile) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	matches := linkNextRe.FindStringSubmatch(resp.Header.Get("Link"))
	if len(matches) >= 2 {
		return matches[1], nil
	}
	return "", nil
}
