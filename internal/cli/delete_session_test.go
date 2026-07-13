package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestRunDeleteSessionDeletesNamedSession(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testSession("chat", "default")).Build()

	var out strings.Builder
	if err := runDeleteSession(ctx, cl, "default", []string{"chat"}, false, &out); err != nil {
		t.Fatalf("runDeleteSession: %v", err)
	}

	err := cl.Get(ctx, client.ObjectKey{Name: "chat", Namespace: "default"}, &kelos.Session{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected session to be deleted, got error %v", err)
	}
	if out.String() != "session/chat deleted\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestRunDeleteSessionAllDeletesSessionsInNamespace(t *testing.T) {
	ctx := context.Background()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		testSession("chat-a", "default"),
		testSession("chat-b", "default"),
		testSession("chat-c", "other"),
	).Build()

	var out strings.Builder
	if err := runDeleteSession(ctx, cl, "default", nil, true, &out); err != nil {
		t.Fatalf("runDeleteSession: %v", err)
	}

	for _, name := range []string{"chat-a", "chat-b"} {
		err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: "default"}, &kelos.Session{})
		if !apierrors.IsNotFound(err) {
			t.Fatalf("expected session %s to be deleted, got error %v", name, err)
		}
	}
	if err := cl.Get(ctx, client.ObjectKey{Name: "chat-c", Namespace: "other"}, &kelos.Session{}); err != nil {
		t.Fatalf("expected session in other namespace to remain: %v", err)
	}

	gotLines := strings.Split(strings.TrimSpace(out.String()), "\n")
	slices.Sort(gotLines)
	wantLines := []string{"session/chat-a deleted", "session/chat-b deleted"}
	if !slices.Equal(gotLines, wantLines) {
		t.Fatalf("unexpected output lines: got %v, want %v", gotLines, wantLines)
	}
}

func TestRunDeleteSessionAllNoSessions(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	var out strings.Builder
	if err := runDeleteSession(context.Background(), cl, "default", nil, true, &out); err != nil {
		t.Fatalf("runDeleteSession: %v", err)
	}
	if out.String() != "No sessions found\n" {
		t.Fatalf("unexpected output: %q", out.String())
	}
}
