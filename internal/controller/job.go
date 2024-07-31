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
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

func initJob(workbench defaultv1alpha1.Workbench, index int, app defaultv1alpha1.WorkbenchApp, service corev1.Service) batchv1.Job {
	job := batchv1.Job{}

	job.Name = fmt.Sprintf("%s-%d-%s", workbench.Name, index, app.Name)
	job.Namespace = workbench.Namespace

	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	job.Labels = labels

	// Fix empty version
	appVersion := app.Version
	if appVersion == "" {
		appVersion = "latest"
	}

	// TODO: allow the registry to be specific as well.
	appImage := fmt.Sprintf("registry.build.chorus-tre.local/%s:%s", app.Name, appVersion)
	appContainer := corev1.Container{
		Name:            app.Name,
		Image:           appImage,
		ImagePullPolicy: "IfNotPresent",
		Env: []corev1.EnvVar{
			{
				Name:  "DISPLAY",
				Value: fmt.Sprintf("%s.%s:80", service.Name, service.Namespace), // FIXME: 80 from 6080
			},
		},
	}

	job.Spec.Template.Spec.Containers = []corev1.Container{
		appContainer,
	}
	// This allows the end user to stop the application from within Xpra.
	job.Spec.Template.Spec.RestartPolicy = "OnFailure"

	return job
}

func (r *WorkbenchReconciler) deleteJobs(ctx context.Context, workbench defaultv1alpha1.Workbench) (int, error) {
	log := log.FromContext(ctx)

	// Find all the jobs linked with the workbench.
	jobList := batchv1.JobList{}

	err := r.List(
		ctx,
		&jobList,
		client.MatchingLabels{matchingLabel: workbench.Name},
	)
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