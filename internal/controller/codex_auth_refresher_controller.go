package controller

import (
	"context"
	"reflect"

	"github.com/kelos-dev/kelos/internal/codexauth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type CodexAuthRefresherReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Builder *CodexAuthRefresherBuilder
}

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch;create;update;patch;delete

func (r *CodexAuthRefresherReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	if r.Builder == nil {
		r.Builder = NewCodexAuthRefresherBuilder()
	}

	var secret corev1.Secret
	if err := r.Get(ctx, req.NamespacedName, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.deleteCronJob(ctx, req.Namespace, req.Name)
		}
		logger.Error(err, "Unable to fetch Secret", "namespace", req.Namespace, "name", req.Name)
		return ctrl.Result{}, err
	}

	if !IsCodexAuthRefreshable(&secret) {
		return ctrl.Result{}, r.deleteCronJob(ctx, secret.Namespace, secret.Name)
	}

	desired := r.Builder.Build(&secret)
	var current batchv1.CronJob
	key := client.ObjectKeyFromObject(desired)
	if err := r.Get(ctx, key, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Unable to fetch Codex auth refresher CronJob", "cronJob", desired.Name)
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, desired); err != nil {
			logger.Error(err, "Unable to create Codex auth refresher CronJob", "cronJob", desired.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Created Codex auth refresher CronJob", "cronJob", desired.Name, "secret", secret.Name, "namespace", secret.Namespace)
		return ctrl.Result{}, nil
	}

	if updateCodexAuthRefresherCronJob(&current, desired) {
		if err := r.Update(ctx, &current); err != nil {
			logger.Error(err, "Unable to update Codex auth refresher CronJob", "cronJob", current.Name)
			return ctrl.Result{}, err
		}
		logger.Info("Updated Codex auth refresher CronJob", "cronJob", current.Name, "secret", secret.Name, "namespace", secret.Namespace)
	}
	return ctrl.Result{}, nil
}

func (r *CodexAuthRefresherReconciler) deleteCronJob(ctx context.Context, secretNamespace, secretName string) error {
	if r.Builder == nil {
		r.Builder = NewCodexAuthRefresherBuilder()
	}
	namespace := r.Builder.Namespace
	if namespace == "" {
		namespace = "kelos-system"
	}
	name := CodexAuthRefresherCronJobName(secretNamespace, secretName)

	var cronJob batchv1.CronJob
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &cronJob); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if cronJob.Annotations[codexAuthSecretNamespaceAnnotation] != secretNamespace || cronJob.Annotations[codexAuthSecretNameAnnotation] != secretName {
		return nil
	}
	return r.Delete(ctx, &cronJob)
}

func updateCodexAuthRefresherCronJob(current, desired *batchv1.CronJob) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Annotations, desired.Annotations) {
		current.Annotations = desired.Annotations
		needsUpdate = true
	}
	if current.Spec.Schedule != desired.Spec.Schedule {
		current.Spec.Schedule = desired.Spec.Schedule
		needsUpdate = true
	}
	if current.Spec.ConcurrencyPolicy != desired.Spec.ConcurrencyPolicy {
		current.Spec.ConcurrencyPolicy = desired.Spec.ConcurrencyPolicy
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.SuccessfulJobsHistoryLimit, desired.Spec.SuccessfulJobsHistoryLimit) {
		current.Spec.SuccessfulJobsHistoryLimit = desired.Spec.SuccessfulJobsHistoryLimit
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.FailedJobsHistoryLimit, desired.Spec.FailedJobsHistoryLimit) {
		current.Spec.FailedJobsHistoryLimit = desired.Spec.FailedJobsHistoryLimit
		needsUpdate = true
	}
	if updateCodexAuthRefresherJobTemplate(&current.Spec.JobTemplate, &desired.Spec.JobTemplate) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherJobTemplate(current, desired *batchv1.JobTemplateSpec) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.BackoffLimit, desired.Spec.BackoffLimit) {
		current.Spec.BackoffLimit = desired.Spec.BackoffLimit
		needsUpdate = true
	}
	if !reflect.DeepEqual(current.Spec.ActiveDeadlineSeconds, desired.Spec.ActiveDeadlineSeconds) {
		current.Spec.ActiveDeadlineSeconds = desired.Spec.ActiveDeadlineSeconds
		needsUpdate = true
	}
	if updateCodexAuthRefresherPodTemplate(&current.Spec.Template, &desired.Spec.Template) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherPodTemplate(current, desired *corev1.PodTemplateSpec) bool {
	needsUpdate := false
	if !reflect.DeepEqual(current.Labels, desired.Labels) {
		current.Labels = desired.Labels
		needsUpdate = true
	}

	currentPodSpec := &current.Spec
	desiredPodSpec := &desired.Spec
	if currentPodSpec.ServiceAccountName != desiredPodSpec.ServiceAccountName {
		currentPodSpec.ServiceAccountName = desiredPodSpec.ServiceAccountName
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.SecurityContext, desiredPodSpec.SecurityContext) {
		currentPodSpec.SecurityContext = desiredPodSpec.SecurityContext
		needsUpdate = true
	}
	if currentPodSpec.RestartPolicy != desiredPodSpec.RestartPolicy {
		currentPodSpec.RestartPolicy = desiredPodSpec.RestartPolicy
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.InitContainers, desiredPodSpec.InitContainers) {
		currentPodSpec.InitContainers = desiredPodSpec.InitContainers
		needsUpdate = true
	}
	if !reflect.DeepEqual(currentPodSpec.Volumes, desiredPodSpec.Volumes) {
		currentPodSpec.Volumes = desiredPodSpec.Volumes
		needsUpdate = true
	}
	if updateCodexAuthRefresherContainers(currentPodSpec, desiredPodSpec) {
		needsUpdate = true
	}
	return needsUpdate
}

func updateCodexAuthRefresherContainers(currentPodSpec, desiredPodSpec *corev1.PodSpec) bool {
	if len(currentPodSpec.Containers) != len(desiredPodSpec.Containers) {
		currentPodSpec.Containers = desiredPodSpec.Containers
		return true
	}
	if len(desiredPodSpec.Containers) == 0 {
		return false
	}

	needsUpdate := false
	for i := range desiredPodSpec.Containers {
		current := &currentPodSpec.Containers[i]
		desired := desiredPodSpec.Containers[i]
		if current.Name != desired.Name {
			current.Name = desired.Name
			needsUpdate = true
		}
		if current.Image != desired.Image {
			current.Image = desired.Image
			needsUpdate = true
		}
		if desired.ImagePullPolicy != "" && current.ImagePullPolicy != desired.ImagePullPolicy {
			current.ImagePullPolicy = desired.ImagePullPolicy
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Command, desired.Command) {
			current.Command = desired.Command
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Args, desired.Args) {
			current.Args = desired.Args
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.Env, desired.Env) {
			current.Env = desired.Env
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.VolumeMounts, desired.VolumeMounts) {
			current.VolumeMounts = desired.VolumeMounts
			needsUpdate = true
		}
		if !reflect.DeepEqual(current.SecurityContext, desired.SecurityContext) {
			current.SecurityContext = desired.SecurityContext
			needsUpdate = true
		}
		if !resourceRequirementsEqual(current.Resources, desired.Resources) {
			current.Resources = desired.Resources
			needsUpdate = true
		}
	}
	return needsUpdate
}

func (r *CodexAuthRefresherReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Secret{}, builder.WithPredicates(secretCodexAuthRefreshPredicate{})).
		Watches(&batchv1.CronJob{}, handler.EnqueueRequestsFromMapFunc(r.requestsForCronJob), builder.WithPredicates(codexAuthRefresherCronJobPredicate{})).
		Complete(r)
}

func (r *CodexAuthRefresherReconciler) requestsForCronJob(_ context.Context, obj client.Object) []reconcile.Request {
	cronJob, ok := obj.(*batchv1.CronJob)
	if !ok {
		return nil
	}
	annotations := cronJob.GetAnnotations()
	secretNamespace := annotations[codexAuthSecretNamespaceAnnotation]
	secretName := annotations[codexAuthSecretNameAnnotation]
	if secretNamespace == "" || secretName == "" {
		return nil
	}
	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{Namespace: secretNamespace, Name: secretName},
	}}
}

type secretCodexAuthRefreshPredicate struct{}

func (secretCodexAuthRefreshPredicate) Create(e event.CreateEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

func (secretCodexAuthRefreshPredicate) Delete(e event.DeleteEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

func (secretCodexAuthRefreshPredicate) Update(e event.UpdateEvent) bool {
	oldSecret, oldOK := e.ObjectOld.(*corev1.Secret)
	newSecret, newOK := e.ObjectNew.(*corev1.Secret)
	return oldOK && newOK && (oldSecret.Labels[codexauth.RefreshLabel] == "true" || newSecret.Labels[codexauth.RefreshLabel] == "true")
}

func (secretCodexAuthRefreshPredicate) Generic(e event.GenericEvent) bool {
	secret, ok := e.Object.(*corev1.Secret)
	return ok && secret.Labels[codexauth.RefreshLabel] == "true"
}

type codexAuthRefresherCronJobPredicate struct{}

func (codexAuthRefresherCronJobPredicate) Create(e event.CreateEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func (codexAuthRefresherCronJobPredicate) Delete(e event.DeleteEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func (codexAuthRefresherCronJobPredicate) Update(e event.UpdateEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.ObjectOld) || isManagedCodexAuthRefresherCronJob(e.ObjectNew)
}

func (codexAuthRefresherCronJobPredicate) Generic(e event.GenericEvent) bool {
	return isManagedCodexAuthRefresherCronJob(e.Object)
}

func isManagedCodexAuthRefresherCronJob(obj client.Object) bool {
	if obj == nil {
		return false
	}
	labels := obj.GetLabels()
	if labels["app.kubernetes.io/component"] != codexAuthRefresherComponentLabel || labels["kelos.dev/managed-by"] != "kelos-controller" {
		return false
	}
	annotations := obj.GetAnnotations()
	return annotations[codexAuthSecretNamespaceAnnotation] != "" && annotations[codexAuthSecretNameAnnotation] != ""
}
