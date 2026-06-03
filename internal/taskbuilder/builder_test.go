package taskbuilder

import (
	"testing"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestBuildTask_ForwardsEffort(t *testing.T) {
	tb := &TaskBuilder{}
	template := &v1alpha1.TaskTemplate{
		Type: "codex",
		Credentials: v1alpha1.Credentials{
			Type: v1alpha1.CredentialTypeAPIKey,
			SecretRef: &v1alpha1.SecretReference{
				Name: "credentials",
			},
		},
		Effort:         "high",
		PromptTemplate: "Fix {{.Title}}",
	}

	task, err := tb.BuildTask("task-1", "default", template, map[string]interface{}{
		"Title": "the bug",
	}, nil)
	if err != nil {
		t.Fatalf("BuildTask() returned error: %v", err)
	}

	if task.Spec.Effort != "high" {
		t.Fatalf("task.Spec.Effort = %q, want %q", task.Spec.Effort, "high")
	}
	if task.Spec.Prompt != "Fix the bug" {
		t.Fatalf("task.Spec.Prompt = %q, want %q", task.Spec.Prompt, "Fix the bug")
	}
}
