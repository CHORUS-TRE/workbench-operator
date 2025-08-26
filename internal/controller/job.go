package controller

import (
	"context"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// Default resource requirements for workbench applications
var defaultResources = corev1.ResourceRequirements{
	Limits: corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse("150m"),
		corev1.ResourceMemory:           resource.MustParse("192Mi"),
		corev1.ResourceEphemeralStorage: resource.MustParse("10Gi"),
	},
	Requests: corev1.ResourceList{
		corev1.ResourceCPU:              resource.MustParse("1m"),
		corev1.ResourceMemory:           resource.MustParse("1Ki"),
		corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
	},
}

func initJob(workbench defaultv1alpha1.Workbench, config Config, uid string, app defaultv1alpha1.WorkbenchApp, service corev1.Service, sharedPVCName string) *batchv1.Job {
	job := &batchv1.Job{}

	// The name of the app is there for human consumption.
	job.Name = fmt.Sprintf("%s-%s-%s", workbench.Name, uid, app.Name)
	job.Namespace = workbench.Namespace

	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	job.Labels = labels

	var shmDir *corev1.Volume
	if app.ShmSize != nil {
		shmDir = &corev1.Volume{
			Name: "shm",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    "Memory",
					SizeLimit: app.ShmSize,
				},
			},
		}

		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, *shmDir)
	}

	// Only add workspace volume if PVC name is provided (JuiceFS driver is available)
	if sharedPVCName != "" {
		// Use the namespace-specific PVC (in same namespace as the pod)
		workspaceData := corev1.Volume{
			Name: "workspace-data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: sharedPVCName, // Now contains namespace-specific PVC name
				},
			},
		}
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, workspaceData)
	}

	// The pod will be cleaned up after a day.
	// https://kubernetes.io/docs/concepts/workloads/controllers/job/#ttl-mechanism-for-finished-jobs
	oneDay := int32(24 * 3600)
	job.Spec.TTLSecondsAfterFinished = &oneDay

	// Service account is an alternative to the image Pull Secrets
	serviceAccountName := workbench.Spec.ServiceAccount
	if serviceAccountName != "" {
		job.Spec.Template.Spec.ServiceAccountName = serviceAccountName
	}

	var appImage string
	imagePullPolicy := corev1.PullIfNotPresent

	if app.Image == nil {
		// Fix empty version
		appVersion := app.Version
		if appVersion == "" {
			appVersion = "latest"
			imagePullPolicy = corev1.PullAlways
		}

		// Non-empty registry requires a / to concatenate with the app one.
		registry := config.Registry
		if registry != "" {
			registry = strings.TrimRight(registry, "/") + "/"
		}

		appsRepository := config.AppsRepository
		if appsRepository != "" {
			appsRepository = strings.Trim(appsRepository, "/") + "/"
		}

		appImage = fmt.Sprintf("%s%s%s:%s", registry, appsRepository, app.Name, appVersion)
	} else {
		// Fix empty version
		appVersion := app.Image.Tag
		if appVersion == "" {
			appVersion = "latest"
			imagePullPolicy = corev1.PullAlways
		}

		appImage = fmt.Sprintf("%s/%s:%s", app.Image.Registry, app.Image.Repository, appVersion)
	}

	// Handle user with default value
	user := workbench.Spec.Server.User
	if user == "" {
		user = "chorus"
	}

	appContainer := corev1.Container{
		Name:            app.Name,
		Image:           appImage,
		ImagePullPolicy: imagePullPolicy,
		Resources:       defaultResources, // Set default resources
		Env: []corev1.EnvVar{
			{
				Name:  "DISPLAY",
				Value: fmt.Sprintf("%s.%s:80", service.Name, service.Namespace), // FIXME: 80 from 6080
			},
			{
				Name:  "CHORUS_USER",
				Value: user,
			},
		},
	}

	// Add kiosk configuration if this is a kiosk app and has kiosk config
	if strings.Contains(appContainer.Image, "apps/kiosk") && app.KioskConfig != nil {
		appContainer.Env = append(appContainer.Env, corev1.EnvVar{
			Name:  "KIOSK_URL",
			Value: app.KioskConfig.URL,
		})
	}

	// Override with custom resources if specified
	if app.Resources != nil {
		if app.Resources.Limits != nil {
			appContainer.Resources.Limits = app.Resources.Limits
		}

		if app.Resources.Requests != nil {
			appContainer.Resources.Requests = app.Resources.Requests

			// If limits aren't specified but requests are, use requests as limits
			if app.Resources.Limits == nil {
				appContainer.Resources.Limits = app.Resources.Requests
			}
		}
	}

	// Mounting the /dev/shm volume.
	if shmDir != nil {
		appContainer.VolumeMounts = append(appContainer.VolumeMounts, corev1.VolumeMount{
			Name:      shmDir.Name,
			MountPath: "/dev/shm",
		})
	}

	// Only mount workspace data volume if PVC name is provided (JuiceFS driver is available)
	if sharedPVCName != "" {
		// Mounting the workspace data volume with namespace-specific subPath
		appContainer.VolumeMounts = append(appContainer.VolumeMounts, corev1.VolumeMount{
			Name:      "workspace-data",
			MountPath: "/home/chorus/workspace-data",
			SubPath:   fmt.Sprintf("workspaces/%s", job.Namespace),
		})
	}

	job.Spec.Template.Spec.Containers = []corev1.Container{
		appContainer,
	}

	for _, imagePullSecret := range workbench.Spec.ImagePullSecrets {
		job.Spec.Template.Spec.ImagePullSecrets = append(job.Spec.Template.Spec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: imagePullSecret,
		})
	}

	// Hide the pod name in favour of the app name.
	job.Spec.Template.Spec.Hostname = app.Name

	// This allows the end user to stop the application from within Xpra.
	job.Spec.Template.Spec.RestartPolicy = "OnFailure"

	appState := app.State
	if appState == "" {
		appState = "Running"
	}

	if appState != "Running" {
		tru := true
		job.Spec.Suspend = &tru
	} else {
		fal := false
		job.Spec.Suspend = &fal
	}

	return job
}

// updateJob  makes the destination batch Job (app), like the source one.
//
// It's not allowed to modify the Job definition outside of suspending it.
func updateJob(source batchv1.Job, destination *batchv1.Job) bool {
	updated := false

	suspend := source.Spec.Suspend
	if suspend != nil && (destination.Spec.Suspend == nil || *destination.Spec.Suspend != *suspend) {
		destination.Spec.Suspend = suspend
		updated = true
	}

	return updated
}

func (r *WorkbenchReconciler) findJobs(ctx context.Context, workbench defaultv1alpha1.Workbench) (*batchv1.JobList, error) {
	jobList := batchv1.JobList{}

	err := r.List(
		ctx,
		&jobList,
		client.MatchingLabels{
			matchingLabel: workbench.Name,
		},
	)

	return &jobList, err
}

// Delete the given job.
func (r *WorkbenchReconciler) deleteJob(ctx context.Context, job *batchv1.Job) error {
	log := log.FromContext(ctx)

	log.V(1).Info("Delete a job", "job", job.Name)

	return r.Delete(
		ctx,
		job,
		client.PropagationPolicy("Background"),
	)
}

// Delete all the jobs of the given workbench.
func (r *WorkbenchReconciler) deleteJobs(ctx context.Context, workbench defaultv1alpha1.Workbench) (int, error) {
	log := log.FromContext(ctx)

	// Find all the jobs linked with the workbench.
	jobList, err := r.findJobs(ctx, workbench)
	if err != nil {
		return 0, err
	}

	// Done.
	if len(jobList.Items) == 0 {
		return 0, nil
	}

	log.V(1).Info("Delete all jobs")

	if err := r.DeleteAllOf(
		ctx,
		&batchv1.Job{},
		client.InNamespace(workbench.Namespace),
		client.PropagationPolicy("Background"),
		client.MatchingLabels{matchingLabel: workbench.Name},
	); err != nil {
		return 0, err
	}

	return len(jobList.Items), nil
}
