package sessionruntime

import (
	"slices"
	"testing"
)

func TestClaudeCommandArgsBypassPermissions(t *testing.T) {
	args := claudeCommandArgs(ProviderConfig{}, "session-id", false)
	if !slices.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("Claude Code arguments = %v, want --dangerously-skip-permissions", args)
	}
	if slices.Contains(args, "--permission-mode") {
		t.Fatalf("Claude Code arguments = %v, want no interactive permission mode", args)
	}
	if !slices.Contains(args, "--permission-prompt-tool") || !slices.Contains(args, "stdio") {
		t.Fatalf("Claude Code arguments = %v, want the stdio prompt bridge", args)
	}
}

func TestCodexThreadParamsBypassApprovalsAndSandbox(t *testing.T) {
	provider := CodexProvider{config: ProviderConfig{WorkingDir: "/workspace/repo"}}
	params := provider.threadParams(map[string]any{})
	if params["approvalPolicy"] != "never" {
		t.Fatalf("approvalPolicy = %v, want never", params["approvalPolicy"])
	}
	if params["sandbox"] != "danger-full-access" {
		t.Fatalf("sandbox = %v, want danger-full-access", params["sandbox"])
	}
	if reviewer, ok := params["approvalsReviewer"]; ok {
		t.Fatalf("approvalsReviewer = %v, want unset", reviewer)
	}
}
