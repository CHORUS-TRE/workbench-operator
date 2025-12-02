package controller

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
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

// getAppNameFromRepository extracts the base app name from an image repository path.
// For example: "apps/freesurfer" -> "freesurfer"
// This is needed because app.Name in the CR may include instance suffixes (e.g., "freesurfer-159")
// but the Dockerfile uses the base name for paths like /apps/freesurfer/config/
func getAppNameFromRepository(repository string) string {
	return strings.TrimPrefix(repository, "apps/")
}

// checkImageForUIDCollisions inspects an image's /etc/passwd for UIDs in the Chorus range (1001-9999)
func checkImageForUIDCollisions(ctx context.Context, imageName string) ([]string, error) {
	log := log.FromContext(ctx)

	// Use docker/podman to inspect the image's /etc/passwd
	// Try docker first, fall back to podman
	var cmd *exec.Cmd
	dockerPath, dockerErr := exec.LookPath("docker")
	if dockerErr == nil {
		cmd = exec.CommandContext(ctx, dockerPath, "run", "--rm", "--entrypoint", "cat", imageName, "/etc/passwd")
	} else {
		podmanPath, podmanErr := exec.LookPath("podman")
		if podmanErr != nil {
			log.V(1).Info("Neither docker nor podman found, skipping UID collision check", "image", imageName)
			return nil, nil // Skip validation if no container runtime available
		}
		cmd = exec.CommandContext(ctx, podmanPath, "run", "--rm", "--entrypoint", "cat", imageName, "/etc/passwd")
	}

	// Set timeout for the command
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd = exec.CommandContext(cmdCtx, cmd.Path, cmd.Args[1:]...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Image might not exist locally, or /etc/passwd doesn't exist
		log.V(1).Info("Could not read /etc/passwd from image", "image", imageName, "error", err.Error())
		return nil, nil // Don't fail validation, just skip the check
	}

	// Parse /etc/passwd and find UIDs in range 1001-9999
	var collisions []string
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}

		username := fields[0]
		uidStr := fields[2]
		uid, err := strconv.Atoi(uidStr)
		if err != nil {
			continue
		}

		// Check if UID is in Chorus user range
		if uid >= 1001 && uid <= 9999 {
			collisions = append(collisions, fmt.Sprintf("UID %d (%s)", uid, username))
		}
	}

	return collisions, nil
}

// validateAppImage ensures the app image is compatible and doesn't have UID collisions
func validateAppImage(ctx context.Context, appImage string) error {
	// The init container uses Ubuntu 24.04 with specific tools (useradd, groupadd, libnss-wrapper)
	// App images must be Ubuntu-based for libc and tool compatibility

	// SECURITY: App images must NOT create users with UIDs in the Chorus user range (1001-9999)
	// This prevents UID collisions that would confuse audit trails when users bypass libnss_wrapper
	//
	// Reserved UID ranges:
	//   0-999:   System users (root, daemon, nobody, etc.)
	//   1001-9999: Chorus users (managed by operator)
	//   10000+:  Available for app-specific users (if needed)
	//
	// Example collision scenario:
	//   1. Workbench user has UID 1234 (alice)
	//   2. App image contains: useradd --uid 1234 vscode-user
	//   3. User bypasses libnss_wrapper: unset LD_PRELOAD
	//   4. whoami returns "vscode-user" instead of "alice"
	//   5. Audit logs show wrong username (security issue for incident response)
	//
	// While this doesn't enable privilege escalation (Kubernetes enforces the real UID),
	// it creates audit trail confusion which is a security concern.

	log := log.FromContext(ctx)
	log.V(1).Info("Validating app image", "image", appImage)

	// Check for UID collisions
	collisions, err := checkImageForUIDCollisions(ctx, appImage)
	if err != nil {
		// Log error but don't fail validation - this is a best-effort check
		log.V(1).Info("Error checking for UID collisions", "image", appImage, "error", err.Error())
		return nil
	}

	if len(collisions) > 0 {
		log.Error(nil, "UID collision detected in app image",
			"image", appImage,
			"collisions", strings.Join(collisions, ", "),
			"reserved_range", "1001-9999",
			"impact", "audit trail confusion when users bypass libnss_wrapper",
			"fix", "Remove users in range 1001-9999 or change UIDs to >= 10000")

		return fmt.Errorf("image %s contains UIDs in reserved Chorus range (1001-9999): %s. "+
			"This causes audit trail confusion. See APP-DEVELOPMENT-GUIDELINES.md for details",
			appImage, strings.Join(collisions, ", "))
	}

	log.V(1).Info("No UID collisions detected", "image", appImage)
	return nil
}

func initJob(ctx context.Context, workbench defaultv1alpha1.Workbench, config Config, uid string, app defaultv1alpha1.WorkbenchApp, service corev1.Service, storageManager *StorageManager) *batchv1.Job {
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

	// Add /home volume to share user creation between init and main container
	// The init container creates the user and home directory, main container uses it
	// Note: The user in /etc/passwd doesn't persist, but the home directory does
	homeDir := &corev1.Volume{
		Name: "home",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, *homeDir)

	// Add storage volumes and mounts from the storage manager
	storageVolumes, storageMounts, err := storageManager.GetVolumeAndMountSpecs(ctx, workbench, workbench.Spec.Server.User, job.Namespace)
	if err != nil {
		log := log.FromContext(ctx)
		log.V(1).Info("Storage setup failed, continuing without storage volumes", "error", err)
	} else {
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, storageVolumes...)
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

	// Pod priority class
	applicationPriorityClassName := config.ApplicationPriorityClassName
	if applicationPriorityClassName != "" {
		job.Spec.Template.Spec.PriorityClassName = applicationPriorityClassName
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

	// Validate that the app image is compatible with the init container
	if err := validateAppImage(ctx, appImage); err != nil {
		log := log.FromContext(ctx)
		log.Error(err, "App image validation failed", "image", appImage)
		// Return nil to skip creating this job - the reconciler will retry
		return nil
	}

	// Security: Main container runs as non-root user with zero capabilities
	// User creation and setup is handled by init container (user-setup)
	// This container starts directly as the target user with no privileged operations
	allowPrivilegeEscalation := false
	userID := int64(workbench.Spec.Server.UserID)
	groupID := int64(1001) // Match FSGroup
	runAsNonRoot := true

	appContainer := corev1.Container{
		Name:            app.Name,
		Image:           appImage,
		ImagePullPolicy: imagePullPolicy,
		Resources:       defaultResources, // Set default resources
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                &userID,
			RunAsGroup:               &groupID,
			RunAsNonRoot:             &runAsNonRoot,
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				// No capabilities added - main container needs none!
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "DISPLAY",
				Value: fmt.Sprintf("%s.%s:80", service.Name, service.Namespace), // FIXME: 80 from 6080
			},
			{
				Name:  "CHORUS_USER",
				Value: workbench.Spec.Server.User,
			},
			{
				Name:  "CHORUS_UID",
				Value: strconv.Itoa(workbench.Spec.Server.UserID),
			},
			{
				Name:  "CHORUS_GROUP",
				Value: "chorus",
			},
			{
				Name:  "CHORUS_GID",
				Value: "1001", // Must match FSGroup for volume permission compatibility
			},
			// NSS wrapper configuration for proper user identity resolution
			// Required for kubectl exec sessions to resolve username correctly
			{
				Name:  "LD_PRELOAD",
				Value: "/usr/lib/x86_64-linux-gnu/libnss_wrapper.so",
			},
			{
				Name:  "NSS_WRAPPER_PASSWD",
				Value: "/home/.chorus-auth/passwd",
			},
			{
				Name:  "NSS_WRAPPER_GROUP",
				Value: "/home/.chorus-auth/group",
			},
			{
				Name:  "HOME",
				Value: fmt.Sprintf("/home/%s", workbench.Spec.Server.User),
			},
			{
				Name:  "USER",
				Value: workbench.Spec.Server.User,
			},
			{
				Name:  "APP_NAME",
				Value: getAppNameFromRepository(app.Image.Repository),
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

	// Mount /home directory (shared with init container for user persistence)
	appContainer.VolumeMounts = append(appContainer.VolumeMounts, corev1.VolumeMount{
		Name:      "home",
		MountPath: "/home",
	})

	// Add storage volume mounts (already retrieved above)
	if err == nil {
		appContainer.VolumeMounts = append(appContainer.VolumeMounts, storageMounts...)
	}

	// Construct init container image (same pattern as xpra-server-image)
	// Security: Uses a separate trusted image (not the app image) to prevent
	// malicious app images from running privileged code.
	initContainerVersion := "latest"
	if workbench.Spec.InitContainer != nil && workbench.Spec.InitContainer.Version != "" {
		initContainerVersion = workbench.Spec.InitContainer.Version
	}

	initContainerImagePullPolicy := corev1.PullIfNotPresent
	if initContainerVersion == "latest" {
		initContainerImagePullPolicy = corev1.PullAlways
	}

	initContainerImage := config.InitContainerImage
	if initContainerImage != "" {
		initContainerImage = fmt.Sprintf("%s:%s", initContainerImage, initContainerVersion)
	} else {
		// Fallback to registry-based construction if not specified
		registry := config.Registry
		if registry != "" {
			registry = strings.TrimRight(registry, "/") + "/"
		}
		initContainerImage = fmt.Sprintf("%sapps/app-init:%s", registry, initContainerVersion)
	}

	// Create init container for user setup and directory creation
	// The init container runs as root with minimal capabilities to create the user,
	// setup directories, and configure the environment. It runs to completion before
	// the main application container starts.
	initContainer := corev1.Container{
		Name:            "app-init",
		Image:           initContainerImage,
		ImagePullPolicy: initContainerImagePullPolicy,
		Command:         []string{"/docker-entrypoint.sh"},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptr.To(int64(0)), // Init container runs as root
			AllowPrivilegeEscalation: &allowPrivilegeEscalation,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add: []corev1.Capability{
					"CHOWN",        // Change file ownership
					"SETUID",       // Set user ID for user creation
					"SETGID",       // Set group ID for group creation
					"DAC_OVERRIDE", // Bypass file permission checks (needed for FSGroup-owned directories)
					"FOWNER",       // Bypass permission checks for file operations
				},
			},
		},
		Env: []corev1.EnvVar{
			{
				Name:  "DISPLAY",
				Value: fmt.Sprintf("%s.%s:80", service.Name, service.Namespace),
			},
			{
				Name:  "CHORUS_USER",
				Value: workbench.Spec.Server.User,
			},
			{
				Name:  "CHORUS_UID",
				Value: strconv.Itoa(workbench.Spec.Server.UserID),
			},
			{
				Name:  "CHORUS_GROUP",
				Value: "chorus",
			},
			{
				Name:  "CHORUS_GID",
				Value: "1001", // Must match FSGroup for volume permission compatibility
			},
			{
				Name:  "APP_NAME",
				Value: app.Name,
			},
			{
				Name:  "APP_CMD",
				Value: "echo 'Init container setup complete'", // Dummy command for init
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("100m"),
				corev1.ResourceMemory:           resource.MustParse("64Mi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("1Gi"),
			},
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:              resource.MustParse("10m"),
				corev1.ResourceMemory:           resource.MustParse("16Mi"),
				corev1.ResourceEphemeralStorage: resource.MustParse("100Mi"),
			},
		},
	}

	// Mount /home directory in init container (shared with main container)
	initContainer.VolumeMounts = append(initContainer.VolumeMounts, corev1.VolumeMount{
		Name:      "home",
		MountPath: "/home",
	})

	// Add storage volume mounts to init container
	if err == nil {
		initContainer.VolumeMounts = append(initContainer.VolumeMounts, storageMounts...)
	}

	// Add init container to pod spec
	job.Spec.Template.Spec.InitContainers = []corev1.Container{
		initContainer,
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

	// Security: Pod-level security context with init container pattern
	// - Init container (user-setup): Runs as root with minimal capabilities to create user
	// - Main container: Runs as non-root user (enforced at container level) with zero capabilities
	// - FSGroup ensures volume files are accessible to group 1001
	// - FSGroupChangePolicy optimizes performance by only changing ownership when needed
	// - Seccomp profile provides syscall filtering for defense in depth
	// Note: runAsNonRoot is NOT set at pod level to allow init container to run as root
	//       Main container enforces runAsNonRoot: true at container level
	namespaceGid := int64(1001) // Default group ID for CHORUS users
	fsGroupChangePolicy := corev1.FSGroupChangeOnRootMismatch
	job.Spec.Template.Spec.SecurityContext = &corev1.PodSecurityContext{
		FSGroup:             &namespaceGid,
		FSGroupChangePolicy: &fsGroupChangePolicy,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

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
