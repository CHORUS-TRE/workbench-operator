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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// +kubebuilder:rbac:groups=core,resources=persistentvolumes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
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

	// ---------- APPS ---------------

	// zeroStorageClass explicitly sets storageClassName: "" as YAML would.
	var zeroStorageClass = new(string) // already "" by default

	// Create namespace-specific PV and PVC
	namespacePVName := fmt.Sprintf("%s-pv", workbench.Namespace)
	namespacePVCName := fmt.Sprintf("%s-pvc", workbench.Namespace)

	// Create PV with JuiceFS CSI driver configuration
	namespacePV := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespacePVName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain,
			StorageClassName:              *zeroStorageClass,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "csi.juicefs.com",
					VolumeHandle: fmt.Sprintf("%s-volume", workbench.Namespace),
					NodePublishSecretRef: &corev1.SecretReference{
						Name:      "juicefs-secret",
						Namespace: "kube-system",
					},
					VolumeAttributes: map[string]string{
						// JuiceFS specific attributes can be added here if needed
					},
				},
			},
		},
	}

	// Create namespace-specific PVC
	namespacePVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespacePVCName,
			Namespace: workbench.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
			VolumeName:       namespacePVName,
			StorageClassName: zeroStorageClass,
		},
	}

	// NOTE: No SetControllerReference for PV (cluster-scoped) or PVC (namespace-scoped but shared across workbenches in namespace)
	// These resources should persist independently of individual workbench lifecycles

	// Create the namespace-specific PV if it doesn't exist
	if err := r.createPV(ctx, *namespacePV); err != nil {
		log.V(1).Error(err, "Error creating the namespace PV", "pv", namespacePV.Name)
		return ctrl.Result{}, err
	}

	// Create the namespace-specific PVC if it doesn't exist
	if err := r.createPVC(ctx, *namespacePVC); err != nil {
		log.V(1).Error(err, "Error creating the namespace PVC", "pvc", namespacePVC.Name)
		return ctrl.Result{}, err
	}

	// List of jobs that were either found or created, the others will be deleted.
	foundJobNames := []string{}

	if workbench.Spec.Apps == nil {
		workbench.Spec.Apps = make(map[string]defaultv1alpha1.WorkbenchApp)
	}

	for uid, app := range workbench.Spec.Apps {
		job := initJob(workbench, r.Config, uid, app, service, namespacePVCName)

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

		// TODO: we could follow the pod as well by following the batch.kubernetes.io/job-name
		statusUpdated := (&workbench).UpdateStatusFromJob(uid, *foundJob)
		if statusUpdated {
			if err := r.Status().Update(ctx, &workbench); err != nil {
				log.V(1).Error(err, "Unable to update the WorkbenchStatus")
			}
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

	return r.deleteDeployments(ctx, *workbench)
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

		return nil, nil
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

// createPV creates a PV if missing.
func (r *WorkbenchReconciler) createPV(ctx context.Context, pv corev1.PersistentVolume) error {
	log := log.FromContext(ctx)

	foundPV := corev1.PersistentVolume{}
	err := r.Get(ctx, types.NamespacedName{Name: pv.Name}, &foundPV)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "PV is not (not) found.", "pv", pv.Name)
			return err
		}

		log.V(1).Info("Creating namespace PV", "pv", pv.Name)

		if err := r.Create(ctx, &pv); err != nil {
			log.V(1).Error(err, "Error creating the PV", "pv", pv.Name)
			return err
		}
	} else {
		log.V(1).Info("Namespace PV already exists", "pv", pv.Name)
	}

	return nil
}

// createPVC creates a PVC if missing.
func (r *WorkbenchReconciler) createPVC(ctx context.Context, pvc corev1.PersistentVolumeClaim) error {
	log := log.FromContext(ctx)

	pvcNamespacedName := types.NamespacedName{
		Name:      pvc.Name,
		Namespace: pvc.Namespace,
	}

	foundPVC := corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, pvcNamespacedName, &foundPVC)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "PVC is not (not) found.", "pvc", pvc.Name)
			return err
		}

		log.V(1).Info("Creating namespace PVC", "pvc", pvc.Name, "namespace", pvc.Namespace)

		if err := r.Create(ctx, &pvc); err != nil {
			log.V(1).Error(err, "Error creating the PVC", "pvc", pvc.Name)
			return err
		}
	} else {
		log.V(1).Info("Namespace PVC already exists", "pvc", pvc.Name, "namespace", pvc.Namespace)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkbenchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&defaultv1alpha1.Workbench{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolume{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
