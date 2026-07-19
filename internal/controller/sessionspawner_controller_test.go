package controller

import (
	"context"
	"testing"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
)

func TestSessionSpawnerReconcileCountsAssociatedSessions(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spawner := &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "default", UID: "spawner-uid", Generation: 3},
	}
	controller := true
	associated := &kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name:            "workers-42",
		Namespace:       "default",
		Labels:          map[string]string{sessionbuilder.LabelSessionSpawner: "spawner-uid"},
		OwnerReferences: []metav1.OwnerReference{{Name: "workers", UID: "spawner-uid", Controller: &controller}},
	}}
	unrelatedLabel := &kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name:      "spoofed-workers-42",
		Namespace: "default",
		Labels:    map[string]string{sessionbuilder.LabelSessionSpawner: "spawner-uid"},
	}}
	unrelatedSpawner := &kelos.Session{ObjectMeta: metav1.ObjectMeta{
		Name:      "other-42",
		Namespace: "default",
		Labels:    map[string]string{sessionbuilder.LabelSessionSpawner: "other"},
	}}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.SessionSpawner{}).
		WithObjects(spawner, associated, unrelatedLabel, unrelatedSpawner).
		Build()
	reconciler := &SessionSpawnerReconciler{Client: k8sClient, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "workers"}}); err != nil {
		t.Fatal(err)
	}

	var updated kelos.SessionSpawner
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "workers"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.ObservedGeneration != 3 {
		t.Fatalf("observedGeneration = %d, want 3", updated.Status.ObservedGeneration)
	}
	if updated.Status.TotalSessions != 1 {
		t.Fatalf("totalSessions = %d, want 1", updated.Status.TotalSessions)
	}
	lastDeliverySucceeded := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if lastDeliverySucceeded != nil {
		t.Fatalf("LastDeliverySucceeded condition = %#v, want absent before first delivery", lastDeliverySucceeded)
	}
}

func TestSessionSpawnerReconcilePreservesWebhookDeliveryCondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	spawner := &kelos.SessionSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "workers", Namespace: "default", Generation: 2},
		Status: kelos.SessionSpawnerStatus{Conditions: []metav1.Condition{{
			Type:               kelos.SessionSpawnerConditionLastDeliverySucceeded,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: 1,
			Reason:             "SessionCreateFailed",
			Message:            "Session creation failed",
		}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.SessionSpawner{}).
		WithObjects(spawner).
		Build()
	reconciler := &SessionSpawnerReconciler{Client: k8sClient, Scheme: scheme}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "workers"}}); err != nil {
		t.Fatal(err)
	}

	var updated kelos.SessionSpawner
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "workers"}, &updated); err != nil {
		t.Fatal(err)
	}
	lastDeliverySucceeded := apiMeta.FindStatusCondition(updated.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
	if lastDeliverySucceeded == nil || lastDeliverySucceeded.Status != metav1.ConditionFalse || lastDeliverySucceeded.Reason != "SessionCreateFailed" || lastDeliverySucceeded.ObservedGeneration != 1 {
		t.Fatalf("LastDeliverySucceeded condition = %#v", lastDeliverySucceeded)
	}
}
