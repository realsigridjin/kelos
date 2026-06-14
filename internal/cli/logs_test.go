package cli

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestResolveTaskPodName(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		task    *kelos.Task
		objects []client.Object
		want    string
		wantErr bool
	}{
		{
			name: "uses newest live pod",
			task: &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status: kelos.TaskStatus{
					PodName: "task-pod-old",
				},
			},
			objects: []client.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "task-pod-old",
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(now.Add(-2 * time.Minute)),
						Labels: map[string]string{
							"kelos.dev/task": "task-1",
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:              "task-pod-new",
						Namespace:         "default",
						CreationTimestamp: metav1.NewTime(now.Add(-1 * time.Minute)),
						Labels: map[string]string{
							"kelos.dev/task": "task-1",
						},
					},
				},
			},
			want: "task-pod-new",
		},
		{
			name: "returns empty when no live pod remains",
			task: &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
				Status: kelos.TaskStatus{
					PodName: "task-pod-old",
					Phase:   kelos.TaskPhaseFailed,
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if len(tt.objects) > 0 {
				builder = builder.WithObjects(tt.objects...)
			}
			cl := builder.Build()

			got, err := resolveTaskPodName(context.Background(), cl, "default", tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveTaskPodName() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("resolveTaskPodName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTerminalTaskPhase(t *testing.T) {
	tests := []struct {
		phase kelos.TaskPhase
		want  bool
	}{
		{phase: kelos.TaskPhasePending, want: false},
		{phase: kelos.TaskPhaseRunning, want: false},
		{phase: kelos.TaskPhaseSucceeded, want: true},
		{phase: kelos.TaskPhaseFailed, want: true},
	}

	for _, tt := range tests {
		if got := isTerminalTaskPhase(tt.phase); got != tt.want {
			t.Fatalf("isTerminalTaskPhase(%q) = %v, want %v", tt.phase, got, tt.want)
		}
	}
}

func TestResolveTaskPodNameAfterWait(t *testing.T) {
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "default"},
		Status: kelos.TaskStatus{
			Phase:   kelos.TaskPhaseRunning,
			PodName: "task-pod-old",
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	podName, err := resolveTaskPodName(context.Background(), cl, "default", task)
	if err != nil {
		t.Fatalf("resolveTaskPodName() error = %v", err)
	}
	if podName != "" {
		t.Fatalf("resolveTaskPodName() = %q, want empty", podName)
	}

	if err := func() error {
		if podName == "" {
			return fmt.Errorf("task %q pod disappeared before logs could be streamed", task.Name)
		}
		return nil
	}(); err == nil || err.Error() != `task "task-1" pod disappeared before logs could be streamed` {
		t.Fatalf("post-wait pod validation error = %v", err)
	}
}
