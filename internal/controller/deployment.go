package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// initDeployment creates the Xpra server deployment.
//
// Xpra listens on port 8080 and starts a X11 socket in the tmp folder.
// That folder is shared with a socat sidecar that turns the socket into a nice
// and shiny TCP listener, on port 6080.
func initDeployment(workbench defaultv1alpha1.Workbench, config Config) appsv1.Deployment {
	deployment := appsv1.Deployment{}
	deployment.Name = fmt.Sprintf("%s-server", workbench.Name)
	deployment.Namespace = workbench.Namespace

	// Labels
	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	deployment.Labels = labels
	deployment.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: labels,
	}
	deployment.Spec.Template.Labels = labels

	// Service account is an alternative to the image Pull Secrets
	serviceAccountName := workbench.Spec.ServiceAccount
	if serviceAccountName != "" {
		deployment.Spec.Template.Spec.ServiceAccountName = serviceAccountName
	}

	// Shared by the containers
	volume := corev1.Volume{
		Name: "x11-unix",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      volume.Name,
			MountPath: "/tmp/.X11-unix",
		},
	}

	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{volume}

	for _, imagePullSecret := range workbench.Spec.ImagePullSecrets {
		deployment.Spec.Template.Spec.ImagePullSecrets = append(deployment.Spec.Template.Spec.ImagePullSecrets, corev1.LocalObjectReference{
			Name: imagePullSecret,
		})
	}

	// socatImage and its pull policy
	socatImage := config.SocatImage
	if socatImage == "" {
		socatImage = "alpine/socat:latest"
	}

	socatImagePullPolicy := corev1.PullIfNotPresent
	if strings.HasSuffix(socatImage, ":latest") || !strings.Contains(socatImage, ":") {
		socatImagePullPolicy = corev1.PullAlways
	}

	// As of Kubernetes 1.29, initContainer + restartPolicy: Always is the right way to
	// do sidecar containers:
	// https://kubernetes.io/docs/concepts/workloads/pods/sidecar-containers/
	always := corev1.ContainerRestartPolicyAlways

	sidecarContainer := corev1.Container{
		Name:            "xpra-server-bind",
		Image:           socatImage,
		ImagePullPolicy: socatImagePullPolicy,
		RestartPolicy:   &always,
		Ports: []corev1.ContainerPort{
			{
				Name:          "x11-socket",
				ContainerPort: 6080,
			},
		},
		Args: []string{
			"TCP-LISTEN:6080,fork,bind=0.0.0.0",
			"UNIX-CONNECT:/tmp/.X11-unix/X80",
		},
		VolumeMounts: volumeMounts,
	}

	// Non-empty registry requires a / to concatenate with the Xpra server one.
	registry := config.Registry
	if registry != "" {
		registry = strings.TrimRight(registry, "/") + "/"
	}

	appsRepository := config.AppsRepository
	if appsRepository != "" {
		appsRepository = strings.Trim(appsRepository, "/") + "/"
	}

	// TODO: put default values via the admission webhook.
	serverVersion := workbench.Spec.Server.Version
	if serverVersion == "" {
		serverVersion = "latest" // nolint:goconst
	}

	xpraServerImage := config.XpraServerImage
	if xpraServerImage == "" {
		xpraServerImage = fmt.Sprintf("%s%s%s", registry, appsRepository, "xpra-server")
	}

	serverImage := fmt.Sprintf("%s:%s", xpraServerImage, serverVersion)

	serverImagePullPolicy := corev1.PullIfNotPresent
	if serverVersion == "latest" {
		serverImagePullPolicy = corev1.PullAlways
	}

	serverContainer := corev1.Container{
		Name:            "xpra-server",
		Image:           serverImage,
		ImagePullPolicy: serverImagePullPolicy,
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: 8080,
			},
		},
		Env: []corev1.EnvVar{
			{
				// Will be needed for GPU.
				Name:  "CARD",
				Value: "",
			},
		},
		VolumeMounts: volumeMounts,
	}

	if workbench.Spec.Server.InitialResolutionWidth != 0 && workbench.Spec.Server.InitialResolutionHeight != 0 {
		initialResolution := fmt.Sprintf("%dx%d", workbench.Spec.Server.InitialResolutionWidth, workbench.Spec.Server.InitialResolutionHeight)
		serverContainer.Env = append(serverContainer.Env, corev1.EnvVar{
			Name:  "INITIAL_RESOLUTION",
			Value: initialResolution,
		})
	}

	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{sidecarContainer}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{serverContainer}

	return deployment
}

// updateDeployment makes the destination deployment (Server) like the source.
func updateDeployment(source appsv1.Deployment, destination *appsv1.Deployment) bool {
	updated := false

	containers := destination.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		destination.Spec.Template.Spec.Containers = source.Spec.Template.Spec.Containers
		updated = true
	}

	serverImage := source.Spec.Template.Spec.Containers[0].Image
	if containers[0].Image != serverImage {
		destination.Spec.Template.Spec.Containers[0].Image = serverImage
		updated = true
	}

	initContainers := destination.Spec.Template.Spec.InitContainers
	if len(initContainers) != 1 {
		destination.Spec.Template.Spec.InitContainers = source.Spec.Template.Spec.InitContainers
		updated = true
	}

	sidecarImage := source.Spec.Template.Spec.InitContainers[0].Image
	if initContainers[0].Image != sidecarImage {
		destination.Spec.Template.Spec.InitContainers[0].Image = sidecarImage
		updated = true
	}

	return updated
}

func (r *WorkbenchReconciler) deleteDeployments(ctx context.Context, workbench defaultv1alpha1.Workbench) (int, error) {
	log := log.FromContext(ctx)

	// Find all the deployments linked with the workbench.
	deploymentList := appsv1.DeploymentList{}

	err := r.List(
		ctx,
		&deploymentList,
		client.MatchingLabels{matchingLabel: workbench.Name},
	)
	if err != nil {
		return 0, err
	}

	// Done.
	if len(deploymentList.Items) == 0 {
		return 0, nil
	}

	log.V(1).Info("Delete all deployments")

	if err := r.DeleteAllOf(
		ctx,
		&appsv1.Deployment{},
		client.InNamespace(workbench.Namespace),
		client.MatchingLabels{matchingLabel: workbench.Name},
	); err != nil {
		return 0, err
	}

	return len(deploymentList.Items), nil
}
