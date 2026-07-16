package sessionruntime

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	clientfake "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/fake"
)

func TestWorkspaceStatusPublisherPatchesLiveSession(t *testing.T) {
	const sessionName = "chat"
	podUID := types.UID("live-pod")
	clientset := clientfake.NewSimpleClientset(&kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "default"},
		Status: kelos.SessionStatus{
			Phase:  kelos.SessionPhaseReady,
			PodUID: podUID,
		},
	})
	publisher, err := NewWorkspaceStatusPublisher(clientset.ApiV1alpha2().Sessions("default"), sessionName, podUID)
	if err != nil {
		t.Fatal(err)
	}
	want := WorkspaceStatus{
		Branch: "feature/session-status",
		PullRequest: &kelos.SessionPullRequest{
			URL:   "https://github.com/kelos-dev/kelos/pull/42",
			State: kelos.SessionPullRequestStateOpen,
		},
	}
	if err := publisher(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Branch != want.Branch || !reflect.DeepEqual(got.Status.PullRequest, want.PullRequest) {
		t.Fatalf("Session workspace status = %#v, want %#v", got.Status, want)
	}
	if got.Status.Phase != kelos.SessionPhaseReady || got.Status.PodUID != podUID {
		t.Fatalf("publisher changed controller-owned status: %#v", got.Status)
	}
}

func TestWorkspaceStatusPublisherRequiresLiveReadyPod(t *testing.T) {
	tests := []struct {
		name          string
		phase         kelos.SessionPhase
		currentPodUID types.UID
	}{
		{name: "replaced Pod", phase: kelos.SessionPhaseReady, currentPodUID: types.UID("replacement-pod")},
		{name: "pending Session", phase: kelos.SessionPhasePending, currentPodUID: types.UID("reporting-pod")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const sessionName = "chat"
			clientset := clientfake.NewSimpleClientset(&kelos.Session{
				ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: "default"},
				Status: kelos.SessionStatus{
					Phase:  tt.phase,
					PodUID: tt.currentPodUID,
				},
			})
			publisher, err := NewWorkspaceStatusPublisher(clientset.ApiV1alpha2().Sessions("default"), sessionName, types.UID("reporting-pod"))
			if err != nil {
				t.Fatal(err)
			}
			if err := publisher(context.Background(), WorkspaceStatus{Branch: "stale"}); err == nil {
				t.Fatal("publisher error = nil, want status patch rejection")
			}
			got, err := clientset.ApiV1alpha2().Sessions("default").Get(context.Background(), sessionName, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Status.Branch != "" || got.Status.PullRequest != nil {
				t.Fatalf("rejected publisher changed Session workspace status: %#v", got.Status)
			}
		})
	}
}
