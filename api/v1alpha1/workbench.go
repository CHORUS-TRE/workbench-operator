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

package v1alpha1

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
)

// UpdateStatusFromDeployment enriches the workbench status based on the deployment.
//
// It's not a *best* practice to do so, but it's very convenient.
func (wb *Workbench) UpdateStatusFromDeployment(deployment appsv1.Deployment) bool {
	updated := false

	revisionString, ok := deployment.Annotations["deployment.kubernetes.io/revision"]
	if !ok {
		revisionString = "-1"
	}

	// metadata are strings
	revision, err := strconv.Atoi(revisionString)
	if err != nil {
		revision = -1
	}

	if revision != wb.Status.Server.Revision {
		wb.Status.Server.Revision = revision
		updated = true
	}

	// It's probably too soon to know, so let's mark it
	// as progressing a live happily.
	if len(deployment.Status.Conditions) == 0 {
		wb.Status.Server.Status = WorkbenchStatusServerStatusProgressing
		updated = true

		return updated
	}

	// See: appsv1.DeploymentConditionType
	condition := deployment.Status.Conditions[0]

	var status WorkbenchStatusServerStatus

	switch condition.Type {
	case "Available":
		status = "Complete"
	case "Progressing":
		if condition.Status != "True" {
			status = "Failed"
		} else if condition.Reason == "NewReplicaSetAvailable" {
			status = "Complete"
		} else {
			status = "Progressing"
		}
	case "ReplicaFailure":
	default:
		status = "Failed"
	}

	if status != wb.Status.Server.Status {
		wb.Status.Server.Status = status
		updated = true
	}

	return updated
}

// UpdateStatusAppFromDeployment enriches the workbench status based on the deployment.
//
// It's not a *best* practice to do so, but it's very convenient.
func (wb *Workbench) UpdateStatusFromJob(index int, job batchv1.Job) bool {
	updated := false

	// Create the missing StatusApp if needed.
	if len(wb.Status.Apps) < index+1 {
		wb.Status.Apps = append(wb.Status.Apps, WorkbenchStatusApp{})
	}

	statusApp := wb.Status.Apps[index]

	// It's probably too soon to know, so let's mark it
	// as progressing a live happily.
	if len(job.Status.Conditions) == 0 {
		statusApp.Status = WorkbenchStatusAppStatusProgressing
		updated = true
		/*
					} else {
			       TODO: adapt this for the batchv1.Job
						condition := job.Status.Conditions[0]

						var status WorkbenchStatusAppStatus

						switch condition.Type {
						case "Complete":
							status = "Complete"
						case "Progressing":
							if condition.Status != "True" {
								status = "Failed"
							} else if condition.Reason == "NewReplicaSetAvailable" {
								status = "Complete"
							} else {
								status = "Progressing"
							}
						case "Failed":
						default:
							status = "Failed"
						}

						if status != statusApp.Status {
							statusApp.Status = status
							updated = true
						}
		*/
	}

	// Save it back
	if updated {
		wb.Status.Apps[index] = statusApp
	}

	return updated
}
