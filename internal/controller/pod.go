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

	corev1 "k8s.io/api/core/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// initPod creates an application pod for the given workbench.
func initPod(workbench defaultv1alpha1.Workbench, index int, app defaultv1alpha1.WorkbenchApp, service corev1.Service) corev1.Pod {
	pod := corev1.Pod{}

	// Run the same app many times, if needed.
	pod.Name = fmt.Sprintf("%s-%d-%s", workbench.Name, index, app.Name)
	pod.Namespace = workbench.Namespace

	// Labels
	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	pod.Labels = labels

	// FIXME: put safeguards in the labels/annotations such that it's not possible to
	// replace an existing app by modifying the CR.

	// Fix empty version
	appVersion := app.Version
	if appVersion == "" {
		appVersion = "latest"
	}

	pod.Spec.RestartPolicy = "OnFailure"
	pod.Spec.Containers = []corev1.Container{
		{
			Name:            app.Name,
			Image:           fmt.Sprintf("registry.build.chorus-tre.local/%s:%s", app.Name, appVersion),
			ImagePullPolicy: "IfNotPresent",
			Env: []corev1.EnvVar{
				{
					Name:  "DISPLAY",
					Value: fmt.Sprintf("%s.%s:80", service.Name, service.Namespace), // 80 from 6080
				},
			},
		},
	}

	return pod
}
