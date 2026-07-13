package cli

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestRunGetSessionGetsNamedSession(t *testing.T) {
	ctx := context.Background()
	session := testSession("chat", "default")
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()

	var out strings.Builder
	if err := runGetSession(ctx, cl, "default", []string{"chat"}, false, "", false, &out); err != nil {
		t.Fatalf("runGetSession: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output lines: %q", out.String())
	}
	if fields := strings.Fields(lines[0]); !slices.Equal(fields, []string{"NAME", "TYPE", "PHASE", "POD", "AGE"}) {
		t.Fatalf("unexpected header fields: %v", fields)
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 || !slices.Equal(fields[:4], []string{"chat", "codex", "Ready", "chat-0"}) {
		t.Fatalf("unexpected session fields: %v", fields)
	}
}

func TestRunGetSessionListsAllNamespaces(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		testSession("chat-a", "team-a"),
		testSession("chat-b", "team-b"),
	).Build()

	var out strings.Builder
	if err := runGetSession(ctx, cl, "default", nil, true, "", false, &out); err != nil {
		t.Fatalf("runGetSession: %v", err)
	}

	output := out.String()
	for _, pattern := range []string{
		`(?m)^NAMESPACE\s+NAME\s+TYPE\s+PHASE\s+POD\s+AGE$`,
		`(?m)^team-a\s+chat-a\s+codex\s+Ready\s+chat-a-0\s+\S+$`,
		`(?m)^team-b\s+chat-b\s+codex\s+Ready\s+chat-b-0\s+\S+$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(output) {
			t.Errorf("output did not match %q:\n%s", pattern, output)
		}
	}
}

func TestRunGetSessionPrintsYAML(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testSession("chat", "default")).Build()

	var out strings.Builder
	if err := runGetSession(ctx, cl, "default", []string{"chat"}, false, "yaml", false, &out); err != nil {
		t.Fatalf("runGetSession: %v", err)
	}

	for _, expected := range []string{"apiVersion: kelos.dev/v1alpha2", "kind: Session", "name: chat"} {
		if !strings.Contains(out.String(), expected) {
			t.Errorf("expected %q in YAML output:\n%s", expected, out.String())
		}
	}
}

func TestRunGetSessionPrintsDetail(t *testing.T) {
	ctx := context.Background()
	session := testSession("chat", "default")
	storageClass := "fast"
	session.Spec.Worker.WorkspaceRef = &kelos.WorkspaceReference{Name: "repo"}
	session.Spec.Worker.AgentConfigRefs = []kelos.AgentConfigReference{{Name: "reviewer"}}
	session.Spec.VolumeClaimTemplate = &corev1.PersistentVolumeClaimSpec{
		StorageClassName: &storageClass,
		Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceStorage: resource.MustParse("10Gi"),
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build()

	var out strings.Builder
	if err := runGetSession(ctx, cl, "default", []string{"chat"}, false, "", true, &out); err != nil {
		t.Fatalf("runGetSession: %v", err)
	}

	for _, pattern := range []string{
		`(?m)^Name:\s+chat$`,
		`(?m)^Workspace:\s+repo$`,
		`(?m)^Agent Config:\s+reviewer$`,
		`(?m)^Storage:\s+10Gi$`,
		`(?m)^Storage Class:\s+fast$`,
		`(?m)^Pod:\s+chat-0$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(out.String()) {
			t.Errorf("detail output did not match %q:\n%s", pattern, out.String())
		}
	}
}

func testSession(name, namespace string) *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         namespace,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Hour)),
		},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type: "codex",
			Credentials: &kelos.Credentials{
				Type:      kelos.CredentialTypeAPIKey,
				SecretRef: &kelos.SecretReference{Name: "codex-credentials"},
			},
		}},
		Status: kelos.SessionStatus{
			Phase:   kelos.SessionPhaseReady,
			PodName: name + "-0",
		},
	}
}
