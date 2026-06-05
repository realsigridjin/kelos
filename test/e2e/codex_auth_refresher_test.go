package e2e

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kelos-dev/kelos/internal/codexauth"
	"github.com/kelos-dev/kelos/internal/controller"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

var _ = Describe("Codex auth refresher", func() {
	f := framework.NewFramework("codex-refresh")

	It("should run from the managed CronJob template without waiting for the schedule", func() {
		secretName := "codex-refresh-credentials"
		jobName := "codex-auth-refresh-now"
		authJSONWithoutRefreshToken := `{"tokens":{"access_token":"not-used"},"last_refresh":"2026-01-01T00:00:00Z"}`

		By("creating a labeled Codex OAuth credentials Secret")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: f.Namespace,
				Labels: map[string]string{
					codexauth.RefreshLabel: "true",
				},
			},
			Data: map[string][]byte{
				"CODEX_AUTH_JSON": []byte(authJSONWithoutRefreshToken),
			},
		}
		_, err := f.Clientset.CoreV1().Secrets(f.Namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred(), "Failed to create Codex auth Secret")

		By("waiting for the controller to create the refresher CronJob")
		cronJobName := controller.CodexAuthRefresherCronJobName(f.Namespace, secretName)
		f.WaitForCronJobCreated(cronJobName)

		By("verifying the managed CronJob runs as the Codex agent UID")
		cronJob, err := f.Clientset.BatchV1().CronJobs(f.Namespace).Get(context.TODO(), cronJobName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred(), "Failed to get Codex auth refresher CronJob")
		podSecurityContext := cronJob.Spec.JobTemplate.Spec.Template.Spec.SecurityContext
		Expect(podSecurityContext).NotTo(BeNil())
		Expect(podSecurityContext.RunAsNonRoot).NotTo(BeNil())
		Expect(*podSecurityContext.RunAsNonRoot).To(BeTrue())
		Expect(podSecurityContext.RunAsUser).NotTo(BeNil())
		Expect(*podSecurityContext.RunAsUser).To(Equal(controller.AgentUID))

		By("suspending the CronJob before creating a manual Job")
		suspendCronJob(f, cronJobName)

		By("creating a one-off Job from the managed CronJob template")
		f.CreateJobFromCronJob(cronJobName, jobName)

		By("waiting for the one-off refresher Job to complete")
		f.WaitForJobCompletion(jobName)

		By("printing refresher logs")
		logs := f.GetJobLogs(jobName)
		GinkgoWriter.Printf("Codex auth refresher logs:\n%s\n", logs)
		Expect(logs).To(ContainSubstring("Skipped Codex OAuth Secret"))

		By("verifying CODEX_AUTH_JSON was not updated for a bundle without refresh_token")
		updated, err := f.Clientset.CoreV1().Secrets(f.Namespace).Get(context.TODO(), secretName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred(), "Failed to get Codex auth Secret")
		Expect(string(updated.Data["CODEX_AUTH_JSON"])).To(Equal(authJSONWithoutRefreshToken))
	})
})

func suspendCronJob(f *framework.Framework, name string) {
	Eventually(func() error {
		cronJob, err := f.Clientset.BatchV1().CronJobs(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		suspend := true
		cronJob.Spec.Suspend = &suspend
		_, err = f.Clientset.BatchV1().CronJobs(f.Namespace).Update(context.TODO(), cronJob, metav1.UpdateOptions{})
		return err
	}, 30*time.Second, time.Second).Should(Succeed())

	Eventually(func() bool {
		cronJob, err := f.Clientset.BatchV1().CronJobs(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		return cronJob.Spec.Suspend != nil && *cronJob.Spec.Suspend
	}, 30*time.Second, time.Second).Should(BeTrue())
}
