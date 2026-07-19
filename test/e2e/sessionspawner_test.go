package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

const (
	sessionSpawnerWebhookSecret = "kelos-e2e-webhook-secret"
	sessionSpawnerIssuesPayload = `{
		"action": "opened",
		"sender": {"login": "testuser"},
		"repository": {"full_name": "org/repo", "name": "repo", "owner": {"login": "org"}},
		"issue": {
			"number": 42,
			"title": "Test Issue",
			"body": "Test body",
			"html_url": "https://github.com/org/repo/issues/42",
			"state": "open",
			"labels": []
		}
	}`
)

var _ = Describe("SessionSpawner", func() {
	f := framework.NewFramework("session-spawner")

	It("creates one Session per distinct GitHub delivery and deduplicates redelivery", func() {
		By("creating a Workspace for spawned Sessions")
		f.CreateWorkspace(&kelos.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "session-spawner-workspace"},
			Spec: kelos.WorkspaceSpec{
				Repo: "https://github.com/kelos-dev/kelos.git",
				Ref:  "main",
			},
		})

		By("creating a SessionSpawner for issues webhooks")
		spawner := f.CreateSessionSpawner(&kelos.SessionSpawner{
			ObjectMeta: metav1.ObjectMeta{Name: "session-spawner"},
			Spec: kelos.SessionSpawnerSpec{
				When: kelos.SessionSpawnerWhen{GitHubWebhook: &kelos.GitHubWebhook{
					Repository: "org/repo",
					Events:     []string{"issues"},
					Filters: []kelos.GitHubWebhookFilter{{
						Event:  "issues",
						Action: "opened",
					}},
				}},
				SessionTemplate: kelos.SessionTemplate{
					SessionSpec: kelos.SessionSpec{
						Worker: kelos.WorkerSpec{
							Type:         "codex",
							Credentials:  &kelos.Credentials{Type: kelos.CredentialTypeNone},
							WorkspaceRef: &kelos.WorkspaceReference{Name: "session-spawner-workspace"},
						},
						InitialBranch: "issue-{{.Number}}",
						InitialPrompt: "Handle {{.Event}} #{{.Number}}: {{.Title}}",
					},
				},
			},
		})
		sessionSelector := fmt.Sprintf("kelos.dev/sessionspawner=%s", spawner.UID)

		webhookURL := framework.StartServicePortForward("kelos-system", "kelos-webhook-github", 8443)
		firstDelivery := f.Namespace + "-delivery-1"

		By("sending the first signed GitHub webhook")
		postSessionSpawnerWebhook(webhookURL, firstDelivery)

		var firstSession kelos.Session
		Eventually(func(g Gomega) {
			sessions, err := f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).List(context.TODO(), metav1.ListOptions{
				LabelSelector: sessionSelector,
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(sessions.Items).To(HaveLen(1))
			firstSession = sessions.Items[0]
		}, time.Minute, time.Second).Should(Succeed())

		Expect(firstSession.Spec.InitialBranch).To(Equal("issue-42"))
		Expect(firstSession.Spec.InitialPrompt).To(Equal("Handle issues #42: Test Issue"))
		controllerRef := metav1.GetControllerOf(&firstSession)
		Expect(controllerRef).NotTo(BeNil())
		Expect(controllerRef.Kind).To(Equal("SessionSpawner"))
		Expect(controllerRef.Name).To(Equal("session-spawner"))

		By("verifying successful delivery status")
		Eventually(func(g Gomega) {
			spawner, err := f.KelosClientset.ApiV1alpha2().SessionSpawners(f.Namespace).Get(context.TODO(), "session-spawner", metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(spawner.Status.TotalSessions).To(Equal(int32(1)))
			g.Expect(spawner.Status.LastSessionName).To(Equal(firstSession.Name))
			condition := apiMeta.FindStatusCondition(spawner.Status.Conditions, kelos.SessionSpawnerConditionLastDeliverySucceeded)
			g.Expect(condition).NotTo(BeNil())
			g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			g.Expect(condition.Reason).To(Equal("SessionCreated"))
		}, time.Minute, time.Second).Should(Succeed())

		By("redelivering the same webhook without creating another Session")
		postSessionSpawnerWebhook(webhookURL, firstDelivery)
		sessions, err := f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: sessionSelector,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(sessions.Items).To(HaveLen(1))
		Expect(sessions.Items[0].Name).To(Equal(firstSession.Name))

		By("sending a distinct delivery and creating a second Session")
		postSessionSpawnerWebhook(webhookURL, f.Namespace+"-delivery-2")
		Eventually(func(g Gomega) {
			sessions, listErr := f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).List(context.TODO(), metav1.ListOptions{
				LabelSelector: sessionSelector,
			})
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(sessions.Items).To(HaveLen(2))

			current, getErr := f.KelosClientset.ApiV1alpha2().SessionSpawners(f.Namespace).Get(context.TODO(), "session-spawner", metav1.GetOptions{})
			g.Expect(getErr).NotTo(HaveOccurred())
			g.Expect(current.Status.TotalSessions).To(Equal(int32(2)))
		}, time.Minute, time.Second).Should(Succeed())
	})
})

func postSessionSpawnerWebhook(webhookURL, deliveryID string) {
	payload := []byte(sessionSpawnerIssuesPayload)
	mac := hmac.New(sha256.New, []byte(sessionSpawnerWebhookSecret))
	_, err := mac.Write(payload)
	Expect(err).NotTo(HaveOccurred())

	request, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
	Expect(err).NotTo(HaveOccurred())
	request.Header.Set("X-GitHub-Event", "issues")
	request.Header.Set("X-GitHub-Delivery", deliveryID)
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))

	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	Expect(err).NotTo(HaveOccurred())
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode).To(Equal(http.StatusOK), "webhook response: %s", body)
}
