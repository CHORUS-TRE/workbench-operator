package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// WorkbenchReconciler reconciles a Workbench object
type WorkbenchReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Config   Config
}

// finalizer used to control the clean up the deployments.
const finalizer = "default.k8s.chorus-tre.ch/finalizer"

// matchingLabel is used to catch all the apps of a workbench.
const matchingLabel = "workbench"

var ErrSuspendedJob = errors.New("suspended job")

// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete;deletecollection
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services/status,verbs=get
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete;deletecollection
// +kubebuilder:rbac:groups=batch,resources=jobs/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=csidrivers,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *WorkbenchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(1).Info("Reconcile", "what", req.NamespacedName)

	// Fetch the workbench to reconcile.
	workbench := defaultv1alpha1.Workbench{}
	if err := r.Get(ctx, req.NamespacedName, &workbench); err != nil {
		// Not found means it's been deleted.
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch the workbench")
		}

		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Manage deletion and finalizers.
	containsFinalizer := controllerutil.ContainsFinalizer(&workbench, finalizer)

	if !workbench.DeletionTimestamp.IsZero() {
		// Object has been deleted
		if containsFinalizer {
			// It first removes the sub-resources, then the finalizer.
			count, err := r.deleteExternalResources(ctx, &workbench)
			if err != nil {
				return ctrl.Result{}, err
			}

			// We will get a resource name may not be empty error otherwise.
			if count > 0 {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			finalizersUpdated := controllerutil.RemoveFinalizer(&workbench, finalizer)
			if finalizersUpdated {
				if err := r.Update(ctx, &workbench); err != nil {
					return ctrl.Result{}, err
				}
			}
		}

		// Stop reconciliation as the object is being deleted.
		return ctrl.Result{}, nil
	}

	// verify that the finalizer exists.
	if !containsFinalizer {
		finalizersUpdated := controllerutil.AddFinalizer(&workbench, finalizer)
		if finalizersUpdated {
			if err := r.Update(ctx, &workbench); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// -------- SERVER ---------------

	// The deployment of Xpra server
	deployment := initDeployment(workbench, r.Config)

	// Link the deployment with the Workbench resource such that we can reconcile it
	// when it's being changed.
	if err := controllerutil.SetControllerReference(&workbench, &deployment, r.Scheme); err != nil {
		log.V(1).Error(err, "Error setting the reference", "deployment", deployment.Name)

		return ctrl.Result{}, err
	}

	foundDeployment, err := r.createDeployment(ctx, deployment)
	if err != nil {
		log.V(1).Error(err, "Error creating the deployment")
	}

	// TODO: to properly follow the deployment we have to dig into the replicaset
	// via metadata.annotations."deployment.kubernetes.io/revision"
	// which is also present on the replica. Then the pods, which can be found via
	// the labels, has said replicas as its owner.
	if foundDeployment != nil {
		statusUpdated := (&workbench).UpdateStatusFromDeployment(*foundDeployment)

		// Update server pod health
		serverHealthUpdated := r.updateServerPodHealth(ctx, &workbench, *foundDeployment)
		statusUpdated = statusUpdated || serverHealthUpdated

		if statusUpdated {
			if err := r.Status().Update(ctx, &workbench); err != nil {
				log.V(1).Error(err, "Unable to update the WorkbenchStatus")
			}
		}

		// -------- SERVER UPDATES ------

		// Update the existing deployment with the model one.
		updated := updateDeployment(deployment, foundDeployment)

		if updated {
			log.V(1).Info("Updating Deployment", "deployment", foundDeployment.Name)

			r.Recorder.Event(
				&workbench,
				"Normal",
				"UpdatingDeployment",
				fmt.Sprintf(
					"Updating deployment %q into the namespace %q",
					deployment.Name,
					deployment.Namespace,
				),
			)

			err2 := r.Update(ctx, foundDeployment)
			if err2 != nil {
				log.V(1).Error(err2, "Unable to update the deployment")
				return ctrl.Result{}, err2
			}
		}
	}

	// ------- SERVICE ---------------

	// The service of the Xpra server
	service := initService(workbench)

	// Link the service with the Workbench resource such that we can reconcile it
	// when it's being changed.
	if err := controllerutil.SetControllerReference(&workbench, &service, r.Scheme); err != nil {
		log.V(1).Error(err, "Error setting the reference", "service", service.Name)
		return ctrl.Result{}, err
	}

	if err := r.createService(ctx, service); err != nil {
		log.V(1).Error(err, "Error creating the service", "service", service.Name)
		return ctrl.Result{}, err
	}

	// The service definition is not affected by the CRD, and the status does have any information from it.

	// ---------- STORAGE ---------------

	// Create storage manager and process enabled storage for the workbench
	storageManager := NewStorageManager(r)
	_, err = storageManager.ProcessEnabledStorage(ctx, workbench)
	if err != nil {
		log.Error(err, "Error processing storage")
		return ctrl.Result{}, err
	}

	// ---------- APPS ---------------

	// List of jobs that were either found or created, the others will be deleted.
	foundJobNames := []string{}

	if workbench.Spec.Apps == nil {
		workbench.Spec.Apps = make(map[string]defaultv1alpha1.WorkbenchApp)
	}

	// Check if xpra server is ready before creating app jobs
	xpraReady := workbench.Status.ServerDeployment.ServerPod != nil &&
		workbench.Status.ServerDeployment.ServerPod.Ready

	if !xpraReady {
		log.V(1).Info("Xpra server not ready yet, skipping app job creation")
		// Requeue to check again later
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	for uid, app := range workbench.Spec.Apps {
		job, err := initJob(ctx, workbench, r.Config, uid, app, service, storageManager)
		if err != nil {
			log.Error(err, "Failed to initialize job", "app", app.Name)
			// Set app status to Failed and continue with other apps
			errorMessage := fmt.Sprintf("Failed to initialize job: %s", err.Error())
			if workbench.SetAppStatusFailed(uid, errorMessage) {
				if err := r.Status().Update(ctx, &workbench); err != nil {
					log.V(1).Error(err, "Unable to update app status to Failed", "app", app.Name)
				}
			}
			continue
		}

		// Link the job with the Workbench resource such that we can reconcile it
		// when it's being changed.
		if err := controllerutil.SetControllerReference(&workbench, job, r.Scheme); err != nil {
			log.V(1).Error(err, "Error setting the reference", "job", job.Name)
			return ctrl.Result{}, err
		}

		foundJob, err := r.createJob(ctx, *job)
		if err != nil {
			// Break the loop as nothing shall be created.
			if errors.Is(err, ErrSuspendedJob) {
				continue
			}

			log.V(1).Error(err, "Error creating the job", "job", job.Name)

			return ctrl.Result{}, err
		}

		foundJobNames = append(foundJobNames, job.Name)

		// Break the loop as the job was created.
		if foundJob == nil {
			continue
		}

		// TODO: move that check to an admission webhook.
		if job.Name != foundJob.Name {
			err := fmt.Errorf("One simply cannot change the application name: %s != %s", job.Name, foundJob.Name)
			return ctrl.Result{}, err
		}

		updated := updateJob(*job, foundJob)

		if updated {
			log.V(1).Info("Updating Job", "job", job.Name)

			// FIXME:when the job is suspended from the outside world , it will likely take a while to shutdown as
			// nobody is listening to the killing signal.
			err2 := r.Update(ctx, foundJob)
			if err2 != nil {
				log.V(1).Error(err2, "Unable to update the job", "job", job.Name)
				return ctrl.Result{}, err2
			}
		}

		// Get pod health message for this job
		message := r.updateAppPodHealth(ctx, &workbench, *foundJob)

		// Check startup timeout: if the job has been in a non-running state
		// for longer than the configured timeout, delete it and report failure.
		appStatus := workbench.Status.Apps[uid]
		isSuspended := foundJob.Spec.Suspend != nil && *foundJob.Spec.Suspend
		if r.Config.ApplicationStartupTimeout > 0 &&
			appStatus.Status != defaultv1alpha1.WorkbenchStatusAppStatusRunning &&
			appStatus.Status != defaultv1alpha1.WorkbenchStatusAppStatusFailed &&
			!isSuspended {
			elapsed := time.Since(foundJob.CreationTimestamp.Time)
			timeout := time.Duration(r.Config.ApplicationStartupTimeout) * time.Second
			if elapsed > timeout {
				message = fmt.Sprintf("Startup timeout: app did not become ready within %s (%s)", timeout, message)
				r.deleteJob(ctx, foundJob)
			}
		}

		// TODO: we could follow the pod as well by following the batch.kubernetes.io/job-name
		statusUpdated := (&workbench).UpdateStatusFromJob(uid, *foundJob, message)
		if statusUpdated {
			if err := r.Status().Update(ctx, &workbench); err != nil {
				log.V(1).Error(err, "Unable to update the WorkbenchStatus")
			}
		}
	}

	allJobs, err := r.findJobs(ctx, workbench)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Sorting the job names to leverage the binary search.
	// It's a small list anyway.
	slices.Sort(foundJobNames)
	for _, job := range allJobs.Items {
		_, found := slices.BinarySearch(foundJobNames, job.Name)
		if found {
			continue
		}

		log.V(1).Info("Extra job found, removing", "job", job.Name)
		if err := r.deleteJob(ctx, &job); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Clean up orphaned status entries for apps that were removed from spec
	if workbench.CleanOrphanedAppStatuses() {
		log.V(1).Info("Cleaned up orphaned app status entries")
		if err := r.Status().Update(ctx, &workbench); err != nil {
			log.V(1).Error(err, "Unable to update WorkbenchStatus after cleanup")
			return ctrl.Result{}, err
		}
	}

	// ---------- OBSERVED GENERATION ---------------
	if workbench.UpdateObservedGeneration() {
		log.V(1).Info("Updated observed generation", "generation", workbench.Status.ObservedGeneration)
		if err := r.Status().Update(ctx, &workbench); err != nil {
			log.V(1).Error(err, "Unable to update observed generation in WorkbenchStatus")
			return ctrl.Result{}, err
		}
	}

	// Requeue if any app is in a transitional state to pick up pod status changes.
	for _, appStatus := range workbench.Status.Apps {
		switch appStatus.Status {
		case defaultv1alpha1.WorkbenchStatusAppStatusProgressing,
			defaultv1alpha1.WorkbenchStatusAppStatusStopping,
			defaultv1alpha1.WorkbenchStatusAppStatusKilling:
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

// deleteExternalResources removes the underlying Deployment(s).
func (r *WorkbenchReconciler) deleteExternalResources(ctx context.Context, workbench *defaultv1alpha1.Workbench) (int, error) {
	// The service is delete automatically due to the owner reference it holds.

	r.Recorder.Event(
		workbench,
		"Normal",
		"DeletingDeployments",
		fmt.Sprintf(
			`Deleting deployment "%s=%s" from the namespace %q`,
			matchingLabel,
			workbench.Name,
			workbench.Namespace,
		),
	)

	// First delete the applications, then the server.

	// Deref so it's not *modifiable*.
	count, err := r.deleteJobs(ctx, *workbench)
	if count > 0 || err != nil {
		return count, err
	}

	count, err = r.deleteDeployments(ctx, *workbench)
	if count > 0 || err != nil {
		return count, err
	}

	return 0, nil
}

func (r *WorkbenchReconciler) createDeployment(ctx context.Context, deployment appsv1.Deployment) (*appsv1.Deployment, error) {
	log := log.FromContext(ctx)

	deploymentNamespacedName := types.NamespacedName{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
	}

	foundDeployment := appsv1.Deployment{}
	err := r.Get(ctx, deploymentNamespacedName, &foundDeployment)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "Deployment is not (not) found.")
			return nil, err
		}

		log.V(1).Info("Creating the deployment", "deployment", deployment.Name)

		if err := r.Create(ctx, &deployment); err != nil {
			log.V(1).Error(err, "Error creating the deployment")
			// It's probably has already been created.
			// FIXME: check that it's indeed the case.
			return nil, err
		}

		return &deployment, nil
	}

	return &foundDeployment, nil
}

// reconcileService creates the service when missing.
func (r *WorkbenchReconciler) createService(ctx context.Context, service corev1.Service) error {
	log := log.FromContext(ctx)

	serviceNamespacedName := types.NamespacedName{
		Name:      service.Name,
		Namespace: service.Namespace,
	}

	foundService := corev1.Service{}

	err := r.Get(ctx, serviceNamespacedName, &foundService)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "Service is not (not) found.")

			return err
		}

		log.V(1).Info("Creating the service", "service", service.Name)

		return r.Create(ctx, &service)
	}

	return nil
}

// createJob creates a job if missing, or returns the existing job.
func (r *WorkbenchReconciler) createJob(ctx context.Context, job batchv1.Job) (*batchv1.Job, error) {
	log := log.FromContext(ctx)

	jobNamespacedName := types.NamespacedName{
		Name:      job.Name,
		Namespace: job.Namespace,
	}

	foundJob := batchv1.Job{}
	err := r.Get(ctx, jobNamespacedName, &foundJob)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "Job is not (not) found.", "job", job.Name)

			return &foundJob, err
		}

		// Do no create a job in the suspended state. It's a feature to have things in the
		// Workbench definitions that do not exist yet.
		if job.Spec.Suspend != nil && *job.Spec.Suspend == true {
			log.V(1).Info("Skip suspended job", "job", job.Name)
			return nil, fmt.Errorf("skipping job %q: %w", job.Name, ErrSuspendedJob)
		}

		log.V(1).Info("New job", "job", job.Name)

		if err := r.Create(ctx, &job); err != nil {
			log.V(1).Error(err, "Error creating the job", "job", job.Name)

			return nil, err
		}

		return nil, nil
	}

	return &foundJob, err
}

// updateServerPodHealth monitors the xpra-server pod and updates status
func (r *WorkbenchReconciler) updateServerPodHealth(
	ctx context.Context,
	workbench *defaultv1alpha1.Workbench,
	deployment appsv1.Deployment,
) bool {
	log := log.FromContext(ctx)

	// Find pods for this deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(workbench.Namespace),
		client.MatchingLabelsSelector{
			Selector: labels.SelectorFromSet(deployment.Spec.Selector.MatchLabels),
		},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		log.V(1).Error(err, "Failed to list pods for deployment", "deployment", deployment.Name)
		return r.setServerPodHealth(workbench, defaultv1alpha1.ServerPodHealth{
			Status:  defaultv1alpha1.ServerPodStatusUnknown,
			Message: "Failed to list pods",
		})
	}

	// Find most recent pod
	var latestPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if latestPod == nil || pod.CreationTimestamp.After(latestPod.CreationTimestamp.Time) {
			latestPod = pod
		}
	}

	if latestPod == nil {
		return r.setServerPodHealth(workbench, defaultv1alpha1.ServerPodHealth{
			Status:  defaultv1alpha1.ServerPodStatusUnknown,
			Message: "No pods found",
		})
	}

	// Check if pod is terminating
	if latestPod.DeletionTimestamp != nil {
		return r.setServerPodHealth(workbench, defaultv1alpha1.ServerPodHealth{
			Status:  defaultv1alpha1.ServerPodStatusTerminating,
			Message: "Pod is terminating",
		})
	}

	// Find xpra-server-bind init container status
	var initContainerStatus *corev1.ContainerStatus
	for i := range latestPod.Status.InitContainerStatuses {
		if latestPod.Status.InitContainerStatuses[i].Name == "xpra-server-bind" {
			initContainerStatus = &latestPod.Status.InitContainerStatuses[i]
			break
		}
	}

	if initContainerStatus == nil {
		return r.setServerPodHealth(workbench, defaultv1alpha1.ServerPodHealth{
			Status:  defaultv1alpha1.ServerPodStatusUnknown,
			Message: "xpra-server-bind init container not found",
		})
	}

	// Find xpra-server container status
	var containerStatus *corev1.ContainerStatus
	for i := range latestPod.Status.ContainerStatuses {
		if latestPod.Status.ContainerStatuses[i].Name == "xpra-server" {
			containerStatus = &latestPod.Status.ContainerStatuses[i]
			break
		}
	}

	if containerStatus == nil {
		return r.setServerPodHealth(workbench, defaultv1alpha1.ServerPodHealth{
			Status:  defaultv1alpha1.ServerPodStatusUnknown,
			Message: "xpra-server container not found",
		})
	}

	// Determine status from both containers
	health := r.determineServerPodHealth(initContainerStatus, containerStatus)

	return r.setServerPodHealth(workbench, health)
}

// determineServerPodHealth maps container statuses to pod health status
func (r *WorkbenchReconciler) determineServerPodHealth(initContainerStatus *corev1.ContainerStatus, serverStatus *corev1.ContainerStatus) defaultv1alpha1.ServerPodHealth {
	// xpra-server-bind runs as a sidecar, check readiness
	if initContainerStatus.Ready {
		return r.determineContainerHealth(serverStatus)
	}

	// Init container not ready
	health := defaultv1alpha1.ServerPodHealth{
		Ready:        false,
		RestartCount: initContainerStatus.RestartCount,
	}

	if initContainerStatus.State.Waiting != nil {
		health.Status = defaultv1alpha1.ServerPodStatusWaiting
		health.Message = fmt.Sprintf("Init container waiting: %s", initContainerStatus.State.Waiting.Reason)
		if initContainerStatus.State.Waiting.Message != "" {
			health.Message = fmt.Sprintf("Init container waiting: %s - %s",
				initContainerStatus.State.Waiting.Reason,
				initContainerStatus.State.Waiting.Message)
		}
	} else if initContainerStatus.State.Running != nil {
		health.Status = defaultv1alpha1.ServerPodStatusStarting
		health.Message = "Init container starting"
	} else if initContainerStatus.State.Terminated != nil {
		health.Status = defaultv1alpha1.ServerPodStatusFailing
		health.Message = fmt.Sprintf("Init container failed with exit code %d: %s",
			initContainerStatus.State.Terminated.ExitCode,
			initContainerStatus.State.Terminated.Reason)
		if initContainerStatus.State.Terminated.Message != "" {
			health.Message = fmt.Sprintf("Init container failed with exit code %d: %s - %s",
				initContainerStatus.State.Terminated.ExitCode,
				initContainerStatus.State.Terminated.Reason,
				initContainerStatus.State.Terminated.Message)
		}
	} else {
		health.Status = defaultv1alpha1.ServerPodStatusUnknown
		health.Message = "Init container state unknown"
	}

	return health
}

// determineContainerHealth maps a single container status to health status
func (r *WorkbenchReconciler) determineContainerHealth(containerStatus *corev1.ContainerStatus) defaultv1alpha1.ServerPodHealth {
	health := defaultv1alpha1.ServerPodHealth{
		Ready:        containerStatus.Ready,
		RestartCount: containerStatus.RestartCount,
	}

	// Check container state
	if containerStatus.State.Waiting != nil {
		health.Status = defaultv1alpha1.ServerPodStatusWaiting
		health.Message = fmt.Sprintf("Waiting: %s", containerStatus.State.Waiting.Reason)
		return health
	}

	if containerStatus.State.Terminated != nil {
		health.Status = defaultv1alpha1.ServerPodStatusTerminated
		health.Message = fmt.Sprintf("Terminated: %s", containerStatus.State.Terminated.Reason)
		return health
	}

	// Container is running
	if containerStatus.State.Running == nil {
		health.Status = defaultv1alpha1.ServerPodStatusUnknown
		health.Message = "Container state unknown"
		return health
	}

	// Ready takes priority - if container is running and ready, it's healthy
	if containerStatus.Ready {
		health.Status = defaultv1alpha1.ServerPodStatusReady
		health.Message = "Container is ready"
		return health
	}

	// Not ready - check for recent restarts
	if containerStatus.RestartCount > 0 {
		startTime := containerStatus.State.Running.StartedAt.Time
		if time.Since(startTime) < 5*time.Minute {
			health.Status = defaultv1alpha1.ServerPodStatusRestarting
			health.Message = fmt.Sprintf("Recently restarted (%d times)", containerStatus.RestartCount)
			return health
		}
	}

	// Running but not ready
	if containerStatus.RestartCount == 0 && time.Since(containerStatus.State.Running.StartedAt.Time) < 2*time.Minute {
		health.Status = defaultv1alpha1.ServerPodStatusStarting
		health.Message = "Container starting up"
	} else {
		health.Status = defaultv1alpha1.ServerPodStatusFailing
		health.Message = "Readiness probe failing"
	}

	return health
}

// setServerPodHealth updates workbench status and returns if changed
func (r *WorkbenchReconciler) setServerPodHealth(workbench *defaultv1alpha1.Workbench, health defaultv1alpha1.ServerPodHealth) bool {
	if workbench.Status.ServerDeployment.ServerPod == nil {
		workbench.Status.ServerDeployment.ServerPod = &health
		return true
	}

	current := workbench.Status.ServerDeployment.ServerPod
	changed := current.Status != health.Status ||
		current.Ready != health.Ready ||
		current.RestartCount != health.RestartCount ||
		current.Message != health.Message

	if changed {
		*current = health
	}

	return changed
}

// updateAppPodHealth monitors the app pod and returns its status message
func (r *WorkbenchReconciler) updateAppPodHealth(
	ctx context.Context,
	workbench *defaultv1alpha1.Workbench,
	job batchv1.Job,
) string {
	log := log.FromContext(ctx)

	// If job is suspended but still has active pods, the pod is being terminated
	if job.Spec.Suspend != nil && *job.Spec.Suspend && job.Status.Active >= 1 {
		return "Pod is terminating"
	}

	// If job is no longer active, derive message from job status
	if job.Status.Active == 0 {
		// Suspended jobs were stopped/killed by the user
		if job.Spec.Suspend != nil && *job.Spec.Suspend {
			return "Job completed"
		}
		// Job finished successfully
		if job.Status.Succeeded >= 1 {
			return "Job completed"
		}
		// Job has failed pods — check conditions for detail
		if job.Status.Failed >= 1 {
			for _, condition := range job.Status.Conditions {
				if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
					if condition.Message != "" {
						return fmt.Sprintf("Job failed: %s", condition.Message)
					}
					if condition.Reason != "" {
						return fmt.Sprintf("Job failed: %s", condition.Reason)
					}
				}
			}
			return "Job failed"
		}
		// Job is not suspended and has no succeeded/failed pods — starting up
		if job.Spec.Suspend == nil || !*job.Spec.Suspend {
			return "Job starting"
		}
		return "Job inactive"
	}

	// Find pods for this job using the standard job-name label
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(workbench.Namespace),
		client.MatchingLabels{
			"batch.kubernetes.io/job-name": job.Name,
		},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		log.V(1).Error(err, "Failed to list pods for job", "job", job.Name)
		return "Failed to list pods"
	}

	// Find most recent pod
	var latestPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if latestPod == nil || pod.CreationTimestamp.After(latestPod.CreationTimestamp.Time) {
			latestPod = pod
		}
	}

	if latestPod == nil {
		return "No pods found"
	}

	// Check if pod is terminating
	if latestPod.DeletionTimestamp != nil {
		return "Pod is terminating"
	}

	// Find the primary app container status (not init containers)
	if len(latestPod.Status.ContainerStatuses) == 0 {
		// Check pod conditions for scheduling issues
		for _, condition := range latestPod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				if condition.Message != "" {
					return fmt.Sprintf("Scheduling: %s - %s", condition.Reason, condition.Message)
				}
				return fmt.Sprintf("Scheduling: %s", condition.Reason)
			}
		}
		return "Container status not available"
	}

	// Use the first container status (the app container)
	containerStatus := &latestPod.Status.ContainerStatuses[0]

	return r.determineAppContainerMessage(containerStatus)
}

// determineAppContainerMessage extracts a message from container status
func (r *WorkbenchReconciler) determineAppContainerMessage(containerStatus *corev1.ContainerStatus) string {
	// Check container state
	if containerStatus.State.Waiting != nil {
		message := fmt.Sprintf("Waiting: %s", containerStatus.State.Waiting.Reason)
		if containerStatus.State.Waiting.Message != "" {
			message = fmt.Sprintf("Waiting: %s - %s",
				containerStatus.State.Waiting.Reason,
				containerStatus.State.Waiting.Message)
		}
		return message
	}

	if containerStatus.State.Terminated != nil {
		message := fmt.Sprintf("Terminated: %s", containerStatus.State.Terminated.Reason)
		if containerStatus.State.Terminated.Message != "" {
			message = fmt.Sprintf("Terminated with exit code %d: %s - %s",
				containerStatus.State.Terminated.ExitCode,
				containerStatus.State.Terminated.Reason,
				containerStatus.State.Terminated.Message)
		} else {
			message = fmt.Sprintf("Terminated with exit code %d: %s",
				containerStatus.State.Terminated.ExitCode,
				containerStatus.State.Terminated.Reason)
		}
		return message
	}

	// Container is running
	if containerStatus.State.Running == nil {
		return "Container state unknown"
	}

	// Check if container is ready
	if containerStatus.Ready {
		return "Container is ready"
	}

	// Container running but not ready
	if containerStatus.RestartCount == 0 && time.Since(containerStatus.State.Running.StartedAt.Time) < 2*time.Minute {
		return "Container starting up"
	}

	return "Readiness probe failing"
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkbenchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&defaultv1alpha1.Workbench{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
