package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/codexauth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCodexAuthRefresherBuilderBuildsPerSecretCronJob(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	builder := NewCodexAuthRefresherBuilder()
	builder.Namespace = "kelos-system"
	builder.Schedule = "15 */4 * * *"
	builder.CodexImage = "codex:test"
	builder.ImagePullPolicy = corev1.PullAlways

	cronJob := builder.Build(secret)

	if cronJob.Namespace != "kelos-system" {
		t.Fatalf("cronJob namespace = %q, want kelos-system", cronJob.Namespace)
	}
	if len(cronJob.Name) > codexAuthRefresherCronJobNameMaxLength {
		t.Fatalf("cronJob name length = %d, want <= %d", len(cronJob.Name), codexAuthRefresherCronJobNameMaxLength)
	}
	if !strings.HasPrefix(cronJob.Name, "kelos-codex-auth-prod-codex-creds-") {
		t.Fatalf("cronJob name = %q", cronJob.Name)
	}
	if got := cronJob.Annotations[codexAuthSecretNamespaceAnnotation]; got != "prod" {
		t.Fatalf("source namespace annotation = %q, want prod", got)
	}
	if got := cronJob.Annotations[codexAuthSecretNameAnnotation]; got != "codex-creds" {
		t.Fatalf("source name annotation = %q, want codex-creds", got)
	}
	if cronJob.Spec.Schedule != "15 */4 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	if cronJob.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Fatalf("concurrencyPolicy = %q, want Forbid", cronJob.Spec.ConcurrencyPolicy)
	}

	podSpec := cronJob.Spec.JobTemplate.Spec.Template.Spec
	if podSpec.ServiceAccountName != "kelos-controller" {
		t.Fatalf("serviceAccountName = %q, want kelos-controller", podSpec.ServiceAccountName)
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(podSpec.Containers))
	}
	container := podSpec.Containers[0]
	if container.Image != "codex:test" {
		t.Fatalf("image = %q, want codex:test", container.Image)
	}
	if got := container.Command; len(got) != 1 || got[0] != "/kelos/kelos-codex-auth-refresh" {
		t.Fatalf("command = %v", got)
	}
	wantArgs := []string{"--namespace=prod", "--secret=codex-creds"}
	if strings.Join(container.Args, ",") != strings.Join(wantArgs, ",") {
		t.Fatalf("args = %v, want %v", container.Args, wantArgs)
	}
}

func TestCodexAuthRefresherReconcilerCreatesCronJobForLabeledSecret(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	reconciler := newCodexAuthRefresherTestReconciler(secret)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var cronJob batchv1.CronJob
	key := types.NamespacedName{Namespace: "kelos-system", Name: CodexAuthRefresherCronJobName("default", "codex")}
	if err := reconciler.Get(ctx, key, &cronJob); err != nil {
		t.Fatalf("getting CronJob: %v", err)
	}
	if cronJob.Spec.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
}

func TestCodexAuthRefresherReconcilerUpdatesCronJobDrift(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	old := NewCodexAuthRefresherBuilder().Build(secret)
	old.Spec.Schedule = "0 0 * * *"
	old.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image = "old"
	reconciler := newCodexAuthRefresherTestReconciler(secret, old)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var cronJob batchv1.CronJob
	key := types.NamespacedName{Namespace: "kelos-system", Name: old.Name}
	if err := reconciler.Get(ctx, key, &cronJob); err != nil {
		t.Fatalf("getting CronJob: %v", err)
	}
	if cronJob.Spec.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	if image := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image; image != CodexImage {
		t.Fatalf("image = %q, want %q", image, CodexImage)
	}
}

func TestCodexAuthRefresherReconcilerIgnoresAPIDefaultedCronJobFields(t *testing.T) {
	secret := codexAuthSecret("default", "codex")
	desired := NewCodexAuthRefresherBuilder().Build(secret)
	current := desired.DeepCopy()
	podSpec := &current.Spec.JobTemplate.Spec.Template.Spec
	terminationGracePeriodSeconds := int64(30)
	enableServiceLinks := true
	podSpec.DNSPolicy = corev1.DNSClusterFirst
	podSpec.SchedulerName = "default-scheduler"
	podSpec.TerminationGracePeriodSeconds = &terminationGracePeriodSeconds
	podSpec.EnableServiceLinks = &enableServiceLinks
	podSpec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	podSpec.Containers[0].TerminationMessagePath = "/dev/termination-log"
	podSpec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("0.010")
	podSpec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("0.1")

	if updateCodexAuthRefresherCronJob(current, desired) {
		t.Fatalf("updateCodexAuthRefresherCronJob() = true, want false for API defaulted fields")
	}
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != terminationGracePeriodSeconds {
		t.Fatalf("terminationGracePeriodSeconds = %v, want API default preserved", podSpec.TerminationGracePeriodSeconds)
	}
	if got := podSpec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Fatalf("imagePullPolicy = %q, want API default preserved", got)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenLabelRemoved(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	secret.Labels = nil
	reconciler := newCodexAuthRefresherTestReconciler(secret, cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "kelos-system", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenSecretDeleted(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler(cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "kelos-system", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenAuthJSONRemoved(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	secret.Data = nil
	reconciler := newCodexAuthRefresherTestReconciler(secret, cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "kelos-system", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerRequestsForManagedCronJob(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler()

	if !isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob should be recognized as managed")
	}

	requests := reconciler.requestsForCronJob(ctx, cronJob)
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	want := types.NamespacedName{Namespace: "prod", Name: "codex-creds"}
	if requests[0].NamespacedName != want {
		t.Fatalf("request = %v, want %v", requests[0].NamespacedName, want)
	}
}

func TestCodexAuthRefresherCronJobPredicateRequiresManagedLabelsAndSourceAnnotations(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)

	cronJob.Annotations = nil
	if isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob without source annotations should not be recognized as managed")
	}

	cronJob = NewCodexAuthRefresherBuilder().Build(secret)
	cronJob.Labels["app.kubernetes.io/component"] = "other"

	if isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob without managed labels should not be recognized as managed")
	}
}

func TestCodexAuthRefresherReconcilerRequestsForCronJobRequiresSourceAnnotations(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler()

	cronJob.Annotations = nil
	if requests := reconciler.requestsForCronJob(context.Background(), cronJob); len(requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(requests))
	}
}

func newCodexAuthRefresherTestReconciler(objects ...client.Object) *CodexAuthRefresherReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	return &CodexAuthRefresherReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Scheme: scheme,
		Builder: &CodexAuthRefresherBuilder{
			Namespace:  "kelos-system",
			Schedule:   DefaultCodexAuthRefreshSchedule,
			CodexImage: CodexImage,
		},
	}
}

func codexAuthSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				codexauth.RefreshLabel: "true",
			},
		},
		Data: map[string][]byte{
			"CODEX_AUTH_JSON": []byte(`{"tokens":{"refresh_token":"refresh"}}`),
		},
	}
}
