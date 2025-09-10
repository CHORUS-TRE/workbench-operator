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

	if revision != wb.Status.ServerDeployment.Revision {
		wb.Status.ServerDeployment.Revision = revision
		updated = true
	}

	// It's probably too soon to know, so let's mark it
	// as progressing a live happily.
	if len(deployment.Status.Conditions) == 0 {
		wb.Status.ServerDeployment.Status = WorkbenchStatusServerStatusProgressing
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

	if status != wb.Status.ServerDeployment.Status {
		wb.Status.ServerDeployment.Status = status
		updated = true
	}

	return updated
}

// UpdateStatusAppFromDeployment enriches the workbench status based on the deployment.
//
// It's not a *best* practice to do so, but it's very convenient.
func (wb *Workbench) UpdateStatusFromJob(uid string, job batchv1.Job) bool {
	if wb.Status.Apps == nil {
		wb.Status.Apps = make(map[string]WorkbenchStatusApp)
	}

	// Grow the map of StatusApps for the new entry.
	if _, ok := wb.Spec.Apps[uid]; !ok {
		wb.Status.Apps[uid] = WorkbenchStatusApp{
			Revision: -1,
			Status:   WorkbenchStatusAppStatusUnknown,
		}
	}

	app := wb.Status.Apps[uid]

	// Default status
	status := app.Status

	if job.Status.Active == 1 {
		if job.Status.Ready != nil && *job.Status.Ready >= 1 {
			status = WorkbenchStatusAppStatusRunning
		} else {
			status = WorkbenchStatusAppStatusProgressing
		}
	} else {
		if job.Status.Succeeded >= 1 {
			status = WorkbenchStatusAppStatusComplete
		} else {
			status = WorkbenchStatusAppStatusFailed
		}
	}

	// Save it back
	if status != app.Status {
		app.Status = status

		wb.Status.Apps[uid] = app
		return true
	}

	return false
}
