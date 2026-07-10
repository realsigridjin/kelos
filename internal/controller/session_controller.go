package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	// DefaultSessionRuntimeImage is the default image used to inject the Session runtime.
	DefaultSessionRuntimeImage = "ghcr.io/kelos-dev/kelos-session-runtime:latest"

	sessionRuntimeVolumeName = "kelos-session-runtime"
	sessionRuntimeMountPath  = "/kelos/bin"
	sessionRuntimeBinary     = sessionRuntimeMountPath + "/kelos-session-runtime"
	sessionClaudeConfigDir   = "/workspace/.kelos/session/claude-config"
	sessionCodexHome         = "/workspace/.kelos/session/codex-home"
	sessionOpenCodeConfigDir = "/workspace/.kelos/session/opencode-config"
	sessionOpenCodeDataDir   = "/workspace/.kelos/session/opencode-data"
	sessionReadyCondition    = "Ready"
)

// SessionReconciler reconciles a Session object.
type SessionReconciler struct {
	client.Client
	Scheme                        *runtime.Scheme
	JobBuilder                    *JobBuilder
	SessionRuntimeImage           string
	SessionRuntimeImagePullPolicy corev1.PullPolicy
	Recorder                      record.EventRecorder
	TokenClient                   *githubapp.TokenClient
}

// +kubebuilder:rbac:groups=kelos.dev,resources=sessions,verbs=get;list;watch
// +kubebuilder:rbac:groups=kelos.dev,resources=sessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile creates and observes the Pod that owns a Session conversation.
func (r *SessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var session kelos.Session
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Unable to fetch Session")
		return ctrl.Result{}, err
	}

	var pod corev1.Pod
	err := r.Get(ctx, client.ObjectKeyFromObject(&session), &pod)
	if apierrors.IsNotFound(err) {
		if session.Status.PodName != "" || session.Status.PodUID != "" {
			return ctrl.Result{}, r.updateSessionStatus(ctx, &session, nil, kelos.SessionPhaseFailed, "Session Pod was lost", "PodLost")
		}
		return r.createSessionPod(ctx, &session)
	}
	if err != nil {
		logger.Error(err, "Unable to fetch Session Pod", "session", session.Name)
		return ctrl.Result{}, err
	}

	if !metav1.IsControlledBy(&pod, &session) {
		message := fmt.Sprintf("Pod %q already exists and is not controlled by this Session", pod.Name)
		return ctrl.Result{}, r.updateSessionStatus(ctx, &session, &pod, kelos.SessionPhaseFailed, message, "PodConflict")
	}

	phase, message, reason := sessionPhaseForPod(&pod)
	if err := r.updateSessionStatus(ctx, &session, &pod, phase, message, reason); err != nil {
		return ctrl.Result{}, err
	}
	result := ctrl.Result{}
	if next, err := r.refreshSessionGitHubAppTokenIfNeeded(ctx, &session, &pod); err != nil {
		logger.Error(err, "Unable to refresh Session GitHub App token", "session", session.Name)
		result.RequeueAfter = tokenRefreshRetryInterval
	} else if next > 0 {
		result.RequeueAfter = next
	}
	return result, nil
}

func (r *SessionReconciler) createSessionPod(ctx context.Context, session *kelos.Session) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	workspace, agentConfig, waitingMessage, err := r.resolveSessionInputs(ctx, session)
	if err != nil {
		message := fmt.Sprintf("Failed to resolve Session configuration: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "ConfigurationInvalid")
		return ctrl.Result{}, err
	}
	if waitingMessage != "" {
		if err := r.updateSessionStatus(ctx, session, nil, kelos.SessionPhasePending, waitingMessage, "WaitingForDependency"); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	pod, configMap, err := r.buildSessionPod(session, workspace, agentConfig)
	if err != nil {
		message := fmt.Sprintf("Failed to build Session Pod: %v", err)
		_ = r.updateSessionStatus(ctx, session, nil, kelos.SessionPhaseFailed, message, "PodBuildFailed")
		return ctrl.Result{}, err
	}

	if configMap != nil {
		if err := controllerutil.SetControllerReference(session, configMap, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting Session owner on plugin ConfigMap: %w", err)
		}
		if err := r.Create(ctx, configMap); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("creating Session plugin ConfigMap: %w", err)
			}
			var existing corev1.ConfigMap
			if err := r.Get(ctx, client.ObjectKeyFromObject(configMap), &existing); err != nil {
				return ctrl.Result{}, fmt.Errorf("getting existing Session plugin ConfigMap: %w", err)
			}
			if !metav1.IsControlledBy(&existing, session) {
				return ctrl.Result{}, fmt.Errorf("plugin ConfigMap %q already exists and is not controlled by this Session", configMap.Name)
			}
		}
	}

	if err := controllerutil.SetControllerReference(session, pod, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting Session owner on Pod: %w", err)
	}
	if err := r.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		logger.Error(err, "Unable to create Session Pod", "session", session.Name)
		return ctrl.Result{}, err
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(session, corev1.EventTypeNormal, "PodCreated", "Created Pod %s for Session", pod.Name)
	}
	if err := r.updateSessionStatus(ctx, session, pod, kelos.SessionPhasePending, "Session Pod is starting", "PodStarting"); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *SessionReconciler) resolveSessionInputs(ctx context.Context, session *kelos.Session) (*kelos.WorkspaceSpec, *kelos.AgentConfigSpec, string, error) {
	var workspace *kelos.WorkspaceSpec
	if ref := session.Spec.Worker.WorkspaceRef; ref != nil {
		var value kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &value); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, fmt.Sprintf("Waiting for Workspace %q", ref.Name), nil
			}
			return nil, nil, "", fmt.Errorf("fetching Workspace %q: %w", ref.Name, err)
		}
		workspace = value.Spec.DeepCopy()
		if workspace.SecretRef != nil {
			resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace)
			if err != nil {
				return nil, nil, "", err
			}
			workspace = resolved
		}
	}

	refs := session.Spec.Worker.AgentConfigRefs
	if len(refs) == 0 {
		return workspace, nil, "", nil
	}

	specs := make([]kelos.AgentConfigSpec, 0, len(refs))
	for _, ref := range refs {
		var value kelos.AgentConfig
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &value); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil, fmt.Sprintf("Waiting for AgentConfig %q", ref.Name), nil
			}
			return nil, nil, "", fmt.Errorf("fetching AgentConfig %q: %w", ref.Name, err)
		}
		specs = append(specs, value.Spec)
	}

	agentConfig := MergeAgentConfigs(specs)
	if len(agentConfig.Skills) > 0 {
		taskReconciler := TaskReconciler{Client: r.Client}
		if err := taskReconciler.validateSkillsAuthSecrets(ctx, session.Namespace, agentConfig.Skills); err != nil {
			return nil, nil, "", err
		}
	}
	if len(agentConfig.MCPServers) > 0 {
		resolved, err := resolveMCPServerSecrets(ctx, r.Client, session.Namespace, agentConfig.MCPServers)
		if err != nil {
			return nil, nil, "", err
		}
		agentConfig.MCPServers = resolved
	}

	return workspace, agentConfig, "", nil
}

func (r *SessionReconciler) resolveSessionGitHubAppToken(ctx context.Context, session *kelos.Session, workspace *kelos.WorkspaceSpec) (*kelos.WorkspaceSpec, error) {
	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: workspace.SecretRef.Name}, &source); err != nil {
		return nil, fmt.Errorf("fetching Workspace Secret %q: %w", workspace.SecretRef.Name, err)
	}
	if !githubapp.IsGitHubApp(source.Data) {
		return workspace, nil
	}
	if r.TokenClient == nil {
		return nil, errors.New("GitHub App Secret detected but TokenClient is not configured")
	}
	credentials, err := githubapp.ParseCredentials(source.Data)
	if err != nil {
		return nil, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}
	tokenClient := sessionGitHubTokenClient(r.TokenClient, workspace.Repo)
	response, err := tokenClient.GenerateInstallationToken(ctx, credentials)
	if err != nil {
		return nil, fmt.Errorf("generating GitHub App installation token: %w", err)
	}

	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tokenSecretName,
			Namespace: session.Namespace,
			Annotations: map[string]string{
				githubAppSecretAnnotation: workspace.SecretRef.Name,
				tokenExpiresAtAnnotation:  response.ExpiresAt.UTC().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{GitHubTokenSecretKey: []byte(response.Token)},
	}
	if err := controllerutil.SetControllerReference(session, tokenSecret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting Session owner on GitHub token Secret: %w", err)
	}
	if err := r.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("creating Session GitHub token Secret: %w", err)
		}
		var existing corev1.Secret
		if err := r.Get(ctx, client.ObjectKeyFromObject(tokenSecret), &existing); err != nil {
			return nil, fmt.Errorf("getting existing Session GitHub token Secret: %w", err)
		}
		if !metav1.IsControlledBy(&existing, session) {
			return nil, fmt.Errorf("GitHub token Secret %q already exists and is not controlled by this Session", tokenSecretName)
		}
		existing.Data = tokenSecret.Data
		existing.Annotations = tokenSecret.Annotations
		if err := r.Update(ctx, &existing); err != nil {
			return nil, fmt.Errorf("updating Session GitHub token Secret: %w", err)
		}
	}
	resolved := workspace.DeepCopy()
	resolved.SecretRef = &kelos.SecretReference{Name: tokenSecretName}
	return resolved, nil
}

func (r *SessionReconciler) refreshSessionGitHubAppTokenIfNeeded(ctx context.Context, session *kelos.Session, pod *corev1.Pod) (time.Duration, error) {
	tokenSecretName := sessionGitHubTokenSecretName(session.Name)
	var tokenSecret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: tokenSecretName}, &tokenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			if !sessionPodUsesSecret(pod, tokenSecretName) {
				return 0, nil
			}
			return r.recreateSessionGitHubAppToken(ctx, session)
		}
		return 0, err
	}
	sourceName := tokenSecret.Annotations[githubAppSecretAnnotation]
	expiresAtText := tokenSecret.Annotations[tokenExpiresAtAnnotation]
	if sourceName == "" || expiresAtText == "" {
		return 0, nil
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresAtText)
	if err != nil {
		return 0, fmt.Errorf("parsing Session GitHub token expiration: %w", err)
	}
	next := time.Until(expiresAt.Add(-tokenRefreshMargin))
	if next > 0 {
		return next, nil
	}
	if r.TokenClient == nil {
		return 0, errors.New("TokenClient is not configured")
	}

	var source corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: sourceName}, &source); err != nil {
		return 0, fmt.Errorf("fetching source GitHub App Secret %q: %w", sourceName, err)
	}
	credentials, err := githubapp.ParseCredentials(source.Data)
	if err != nil {
		return 0, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}
	repo := ""
	if ref := session.Spec.Worker.WorkspaceRef; ref != nil {
		var workspace kelos.Workspace
		if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: ref.Name}, &workspace); err != nil {
			return 0, fmt.Errorf("fetching Workspace %q for token refresh: %w", ref.Name, err)
		}
		repo = workspace.Spec.Repo
	}
	response, err := sessionGitHubTokenClient(r.TokenClient, repo).GenerateInstallationToken(ctx, credentials)
	if err != nil {
		return 0, fmt.Errorf("refreshing GitHub App installation token: %w", err)
	}
	if tokenSecret.Data == nil {
		tokenSecret.Data = map[string][]byte{}
	}
	tokenSecret.Data[GitHubTokenSecretKey] = []byte(response.Token)
	tokenSecret.Annotations[tokenExpiresAtAnnotation] = response.ExpiresAt.UTC().Format(time.RFC3339)
	if err := r.Update(ctx, &tokenSecret); err != nil {
		return 0, fmt.Errorf("updating Session GitHub token Secret: %w", err)
	}
	return time.Until(response.ExpiresAt.Add(-tokenRefreshMargin)), nil
}

func sessionPodUsesSecret(pod *corev1.Pod, name string) bool {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == GitHubTokenVolumeName && volume.Secret != nil && volume.Secret.SecretName == name {
			return true
		}
	}
	return false
}

func (r *SessionReconciler) recreateSessionGitHubAppToken(ctx context.Context, session *kelos.Session) (time.Duration, error) {
	if session.Spec.Worker.WorkspaceRef == nil {
		return 0, nil
	}
	var workspace kelos.Workspace
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Spec.Worker.WorkspaceRef.Name}, &workspace); err != nil {
		return 0, fmt.Errorf("fetching Workspace %q for Session GitHub token recovery: %w", session.Spec.Worker.WorkspaceRef.Name, err)
	}
	if workspace.Spec.SecretRef == nil {
		return 0, nil
	}
	resolved, err := r.resolveSessionGitHubAppToken(ctx, session, workspace.Spec.DeepCopy())
	if err != nil {
		return 0, err
	}
	if resolved.SecretRef.Name != sessionGitHubTokenSecretName(session.Name) {
		return 0, nil
	}
	return tokenRefreshRetryInterval, nil
}

func sessionGitHubTokenClient(base *githubapp.TokenClient, repo string) *githubapp.TokenClient {
	client := &githubapp.TokenClient{BaseURL: base.BaseURL, Client: base.Client}
	if host, _, _ := parseGitHubRepo(repo); host != "" {
		if apiURL := gitHubAPIBaseURL(host); apiURL != "" {
			client.BaseURL = apiURL
		}
	}
	return client
}

func sessionGitHubTokenSecretName(sessionName string) string {
	return truncateResourceName(sessionName + "-session-github-token")
}

func (r *SessionReconciler) buildSessionPod(session *kelos.Session, workspace *kelos.WorkspaceSpec, agentConfig *kelos.AgentConfigSpec) (*corev1.Pod, *corev1.ConfigMap, error) {
	worker := session.Spec.Worker.DeepCopy()
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace},
		Spec: kelos.TaskSpec{
			Worker: worker,
			Prompt: "session",
		},
	}

	job, err := r.JobBuilder.Build(task, workspace, agentConfig, "session")
	if err != nil {
		return nil, nil, err
	}

	var configMap *corev1.ConfigMap
	if agentConfig != nil && len(agentConfig.Plugins) > 0 {
		configMap, err = buildPluginConfigMap(task, agentConfig.Plugins)
		if err != nil {
			return nil, nil, err
		}
		configMap.Name = sessionPluginConfigMapName(session)
	}

	podSpec := *job.Spec.Template.Spec.DeepCopy()
	if configMap != nil {
		found := false
		for i := range podSpec.Volumes {
			volume := &podSpec.Volumes[i]
			if volume.Name != PluginStagingVolumeName || volume.ConfigMap == nil {
				continue
			}
			volume.ConfigMap.Name = configMap.Name
			found = true
			break
		}
		if !found {
			return nil, nil, errors.New("agent Pod has no plugin ConfigMap volume")
		}
	}
	podSpec.RestartPolicy = corev1.RestartPolicyAlways
	podSpec.ActiveDeadlineSeconds = job.Spec.ActiveDeadlineSeconds
	if podSpec.SecurityContext == nil {
		podSpec.SecurityContext = &corev1.PodSecurityContext{}
	}
	if podSpec.SecurityContext.FSGroup == nil {
		agentUID := AgentUID
		podSpec.SecurityContext.FSGroup = &agentUID
	}

	if len(podSpec.Containers) == 0 {
		return nil, nil, fmt.Errorf("agent Pod has no containers")
	}
	mainContainer := &podSpec.Containers[0]
	mainContainer.Command = []string{sessionRuntimeBinary}
	mainContainer.Args = []string{"serve"}
	switch worker.Type {
	case "claude-code":
		setSessionContainerEnv(mainContainer, "CLAUDE_CONFIG_DIR", sessionClaudeConfigDir)
	case "codex":
		setSessionContainerEnv(mainContainer, "CODEX_HOME", sessionCodexHome)
	case "opencode":
		setSessionContainerEnv(mainContainer, "OPENCODE_CONFIG_DIR", sessionOpenCodeConfigDir)
		setSessionContainerEnv(mainContainer, "XDG_DATA_HOME", sessionOpenCodeDataDir)
	}
	mainContainer.ReadinessProbe = &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{sessionRuntimeBinary, "health"}}},
		InitialDelaySeconds: 1,
		PeriodSeconds:       2,
		TimeoutSeconds:      1,
		FailureThreshold:    15,
	}
	mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, corev1.VolumeMount{
		Name:      sessionRuntimeVolumeName,
		MountPath: sessionRuntimeMountPath,
		ReadOnly:  true,
	})

	hasWorkspace := false
	for _, volume := range podSpec.Volumes {
		if volume.Name == WorkspaceVolumeName {
			hasWorkspace = true
			break
		}
	}
	if !hasWorkspace {
		podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
			Name:         WorkspaceVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		mainContainer.VolumeMounts = append(mainContainer.VolumeMounts, corev1.VolumeMount{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		})
		mainContainer.WorkingDir = WorkspaceMountPath
	}

	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name:         sessionRuntimeVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	podSpec.InitContainers = append([]corev1.Container{{
		Name:            "kelos-session-runtime",
		Image:           r.SessionRuntimeImage,
		ImagePullPolicy: r.SessionRuntimeImagePullPolicy,
		Args:            []string{"--self-copy", sessionRuntimeBinary},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
			RunAsNonRoot: ptr.To(true),
			SeccompProfile: &corev1.SeccompProfile{
				Type: corev1.SeccompProfileTypeRuntimeDefault,
			},
		},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      sessionRuntimeVolumeName,
			MountPath: sessionRuntimeMountPath,
		}},
	}}, podSpec.InitContainers...)
	if podSpec.ServiceAccountName == "" {
		podSpec.AutomountServiceAccountToken = ptr.To(false)
	}

	labels := make(map[string]string, len(job.Labels)+1)
	for key, value := range job.Labels {
		if key != "kelos.dev/task" {
			labels[key] = value
		}
	}
	labels["kelos.dev/component"] = "session"
	labels["kelos.dev/session"] = sessionLabelValue(session)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      session.Name,
			Namespace: session.Namespace,
			Labels:    labels,
		},
		Spec: podSpec,
	}
	return pod, configMap, nil
}

func setSessionContainerEnv(container *corev1.Container, name, value string) {
	for i := range container.Env {
		if container.Env[i].Name == name {
			container.Env[i].Value = value
			container.Env[i].ValueFrom = nil
			return
		}
	}
	container.Env = append(container.Env, corev1.EnvVar{Name: name, Value: value})
}

func sessionPluginConfigMapName(session *kelos.Session) string {
	identity := string(session.UID)
	if identity == "" {
		identity = session.Namespace + "/" + session.Name
	}
	sum := sha256.Sum256([]byte(identity))
	return "session-" + hex.EncodeToString(sum[:16]) + "-plugins"
}

func sessionLabelValue(session *kelos.Session) string {
	if len(session.Name) <= 63 {
		return session.Name
	}
	if session.UID != "" && len(session.UID) <= 63 {
		return string(session.UID)
	}
	sum := sha256.Sum256([]byte(session.Name))
	return hex.EncodeToString(sum[:16])
}

func sessionPhaseForPod(pod *corev1.Pod) (kelos.SessionPhase, string, string) {
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		message := pod.Status.Message
		if message == "" {
			message = fmt.Sprintf("Session Pod entered phase %s", pod.Status.Phase)
		}
		return kelos.SessionPhaseFailed, message, "PodTerminated"
	}
	if message, reason, failed := sessionContainerFailure(pod); failed {
		return kelos.SessionPhaseFailed, message, reason
	}
	if condition := findPodCondition(pod.Status.Conditions, corev1.PodReady); condition != nil && condition.Status == corev1.ConditionTrue {
		return kelos.SessionPhaseReady, "Session runtime is ready", "RuntimeReady"
	}
	return kelos.SessionPhasePending, "Session Pod is starting", "PodStarting"
}

func sessionContainerFailure(pod *corev1.Pod) (string, string, bool) {
	failureReasons := map[string]struct{}{
		"CrashLoopBackOff":           {},
		"CreateContainerConfigError": {},
		"CreateContainerError":       {},
		"ErrImagePull":               {},
		"ImagePullBackOff":           {},
		"InvalidImageName":           {},
		"RunContainerError":          {},
	}
	statusGroups := [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses}
	for _, statuses := range statusGroups {
		for _, status := range statuses {
			waiting := status.State.Waiting
			if waiting == nil {
				continue
			}
			if _, failed := failureReasons[waiting.Reason]; !failed {
				continue
			}
			message := fmt.Sprintf("Session container %q is waiting: %s", status.Name, waiting.Reason)
			if waiting.Message != "" {
				message += ": " + waiting.Message
			}
			return message, waiting.Reason, true
		}
	}
	return "", "", false
}

func findPodCondition(conditions []corev1.PodCondition, conditionType corev1.PodConditionType) *corev1.PodCondition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func (r *SessionReconciler) updateSessionStatus(ctx context.Context, session *kelos.Session, pod *corev1.Pod, phase kelos.SessionPhase, message, reason string) error {
	original := session.Status.DeepCopy()
	session.Status.ObservedGeneration = session.Generation
	session.Status.Phase = phase
	session.Status.Message = message
	if pod != nil {
		session.Status.PodName = pod.Name
		session.Status.PodUID = pod.UID
	}
	conditionStatus := metav1.ConditionFalse
	if phase == kelos.SessionPhaseReady {
		conditionStatus = metav1.ConditionTrue
	}
	apiMeta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{
		Type:               sessionReadyCondition,
		Status:             conditionStatus,
		ObservedGeneration: session.Generation,
		Reason:             reason,
		Message:            message,
	})

	if reflect.DeepEqual(*original, session.Status) {
		return nil
	}
	if err := r.Status().Update(ctx, session); err != nil {
		return fmt.Errorf("updating Session %q status: %w", session.Name, err)
	}
	return nil
}

// SetupWithManager sets up the Session controller with the Manager.
func (r *SessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kelos.Session{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
