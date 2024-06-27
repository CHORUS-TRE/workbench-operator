/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// initDeployment creates the Xpra server deployment.
//
// Xpra listens on port 8080 and starts a X11 socket in the tmp folder.
// That folder is shared with a socat sidecar that turns the socket into a nice
// and shiny TCP listener, on port 6080.
func initDeployment(workbench defaultv1alpha1.Workbench) appsv1.Deployment {
	deployment := appsv1.Deployment{}
	deployment.Name = workbench.Name
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

	sidecarContainer := corev1.Container{
		Name:            "xpra-server-bind",
		Image:           "alpine/socat:1.8.0.0",
		ImagePullPolicy: "IfNotPresent",
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

	// TODO: put default values via the admission webhook.
	serverVersion := workbench.Spec.Server.Version
	if serverVersion == "" {
		serverVersion = "latest"
	}

	// TODO: allow the registry to be specifiec as well.
	serverImage := fmt.Sprintf("registry.build.chorus-tre.local/xpra-server:%s", serverVersion)
	serverContainer := corev1.Container{
		Name:            "xpra-server",
		Image:           serverImage,
		ImagePullPolicy: "IfNotPresent",
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

	// FIXME: Kubernetes 1.29 supports native sidecars as initContainers.
	//
	// always := corev1.ContainerRestartPolicyAlways
	// sidecarContainer.RestartPolicy = &always
	// deployment.Spec.Template.Spec.InitContainers = []{sidecarContainer}
	// deployment.Spec.Template.Spec.Containers = []{serverContainer}
	//
	// we will use less reliable ones in the meantime.
	deployment.Spec.Template.Spec.Containers = []corev1.Container{
		serverContainer,
		sidecarContainer,
	}

	return deployment
}

// updateDeployment makes the destination workbench like the source.
func updateDeployment(source appsv1.Deployment, destination *appsv1.Deployment) bool {
	updated := false

	containers := destination.Spec.Template.Spec.Containers
	if len(containers) != 2 {
		destination.Spec.Template.Spec.Containers = source.Spec.Template.Spec.Containers
		updated = true
	}

	serverImage := source.Spec.Template.Spec.Containers[0].Image
	if containers[0].Image != serverImage {
		destination.Spec.Template.Spec.Containers[0].Image = serverImage
		updated = true
	}

	sidecarImage := source.Spec.Template.Spec.Containers[1].Image
	if containers[1].Image != sidecarImage {
		destination.Spec.Template.Spec.Containers[1].Image = sidecarImage
		updated = true
	}

	return updated
}