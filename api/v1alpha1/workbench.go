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
		status = "Running" // nolint:goconst
	case "Progressing":
		if condition.Status != "True" {
			status = "Failed"
		} else if condition.Reason == "NewReplicaSetAvailable" {
			status = "Running"
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
	// Create the missing StatusApp if needed.
	if len(wb.Status.Apps) < index+1 {
		wb.Status.Apps = append(wb.Status.Apps, WorkbenchStatusApp{})
	}

	app := wb.Status.Apps[index]

	// Default status
	status := app.Status

	if job.Status.Active == 1 {
		if job.Status.Ready != nil && *job.Status.Ready >= 1 {
			status = "Running"
		}
	} else {
		if job.Status.Succeeded >= 1 {
			status = "Complete"
		}
	}

	// Save it back
	if status != app.Status {
		app.Status = status

		wb.Status.Apps[index] = app
		return true
	}

	return false
}
