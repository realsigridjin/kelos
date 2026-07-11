package integration

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("Session", func() {
	var namespace string

	BeforeEach(func() {
		namespace = fmt.Sprintf("session-%d", time.Now().UnixNano())
		Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("creates one owned Pod and becomes ready with it", func() {
		session := validSession(namespace, "chat", "claude-code")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())

		var pod corev1.Pod
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: session.Name}, &pod)).To(Succeed())
			g.Expect(metav1.IsControlledBy(&pod, session)).To(BeTrue())
			g.Expect(pod.Spec.Containers[0].Command).To(Equal([]string{"/kelos/bin/kelos-session-runtime"}))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

		pod.Status.Phase = corev1.PodRunning
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		Expect(k8sClient.Status().Update(ctx, &pod)).To(Succeed())

		Eventually(func(g Gomega) {
			var current kelos.Session
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(session), &current)).To(Succeed())
			g.Expect(current.Status.Phase).To(Equal(kelos.SessionPhaseReady))
			g.Expect(current.Status.PodName).To(Equal(session.Name))
		}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
	})

	It("accepts only the initial providers", func() {
		opencode := validSession(namespace, "opencode", "opencode")
		Expect(k8sClient.Create(ctx, opencode)).To(Succeed())

		unsupported := validSession(namespace, "unsupported", "gemini")
		Expect(k8sClient.Create(ctx, unsupported)).NotTo(Succeed())

		missingCredentials := validSession(namespace, "missing-credentials", "codex")
		missingCredentials.Spec.Worker.Credentials = nil
		Expect(k8sClient.Create(ctx, missingCredentials)).NotTo(Succeed())
	})

	It("requires a spec", func() {
		session := &unstructured.Unstructured{}
		session.SetAPIVersion("kelos.dev/v1alpha2")
		session.SetKind("Session")
		session.SetNamespace(namespace)
		session.SetName("missing-spec")
		Expect(k8sClient.Create(ctx, session)).NotTo(Succeed())
	})

	It("keeps the spec immutable", func() {
		session := validSession(namespace, "immutable", "codex")
		Expect(k8sClient.Create(ctx, session)).To(Succeed())
		session.Spec.Worker.Model = "another-model"
		Expect(k8sClient.Update(ctx, session)).NotTo(Succeed())
	})
})

func validSession(namespace, name, provider string) *kelos.Session {
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
			Type: provider,
			Credentials: &kelos.Credentials{
				Type: kelos.CredentialTypeNone,
			},
		}},
	}
}
