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
func (wb *Workbench) UpdateStatusFromJob(uid string, job batchv1.Job, message string) bool {
	if wb.Status.Apps == nil {
		wb.Status.Apps = make(map[string]WorkbenchStatusApp)
	}

	// Grow the map of StatusApps for the new entry.
	if _, ok := wb.Spec.Apps[uid]; !ok {
		wb.Status.Apps[uid] = WorkbenchStatusApp{
			Revision: -1,
			Status:   WorkbenchStatusAppStatusUnknown,
			Message:  message,
		}
	}

	app := wb.Status.Apps[uid]

	// Default status
	status := app.Status

	// If job is suspended, the user requested stop/kill — this takes priority
	// regardless of how the pod exited (succeeded, failed, OOMKilled, etc.)
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		specApp, exists := wb.Spec.Apps[uid]
		if exists && specApp.State == WorkbenchAppStateStopped {
			if job.Status.Active >= 1 {
				status = WorkbenchStatusAppStatusStopping
			} else {
				status = WorkbenchStatusAppStatusStopped
			}
		} else if exists && specApp.State == WorkbenchAppStateKilled {
			if job.Status.Active >= 1 {
				status = WorkbenchStatusAppStatusKilling
			} else {
				status = WorkbenchStatusAppStatusKilled
			}
		} else if job.Status.Active >= 1 {
			status = WorkbenchStatusAppStatusProgressing
		} else {
			status = WorkbenchStatusAppStatusComplete
		}
	} else if job.Status.Active == 1 {
		if job.Status.Ready != nil && *job.Status.Ready >= 1 {
			status = WorkbenchStatusAppStatusRunning
		} else {
			status = WorkbenchStatusAppStatusProgressing
		}
	} else {
		if job.Status.Succeeded >= 1 {
			status = WorkbenchStatusAppStatusComplete
		} else if job.Status.Failed >= 1 {
			status = WorkbenchStatusAppStatusFailed
		} else {
			// No active, succeeded, or failed pods — job is starting up
			status = WorkbenchStatusAppStatusProgressing
		}
	}

	// Determine the final message to use
	finalMessage := message
	// If status is Failed and new message is generic "Job failed",
	// preserve the previous detailed message if it exists.
	// Do NOT preserve generic transitional messages (e.g. "Job starting").
	if status == WorkbenchStatusAppStatusFailed && message == "Job failed" && app.Message != "" && app.Message != "Job failed" {
		switch app.Message {
		case "Job starting", "Job inactive", "Job completed", "No pods found", "Container status not available":
			// These are generic/transitional — don't preserve them
		default:
			finalMessage = app.Message
		}
	}

	// Save it back if status or message changed
	if status != app.Status || finalMessage != app.Message {
		app.Status = status
		app.Message = finalMessage

		wb.Status.Apps[uid] = app
		return true
	}

	return false
}

// SetAppStatusFailed sets an app's status to Failed when job initialization fails.
// This is used when the job cannot be created (e.g., missing image configuration).
func (wb *Workbench) SetAppStatusFailed(uid string, message string) bool {
	if wb.Status.Apps == nil {
		wb.Status.Apps = make(map[string]WorkbenchStatusApp)
	}

	app, exists := wb.Status.Apps[uid]
	if !exists {
		app = WorkbenchStatusApp{Revision: -1}
	}

	if app.Status != WorkbenchStatusAppStatusFailed || app.Message != message {
		app.Status = WorkbenchStatusAppStatusFailed
		app.Message = message
		wb.Status.Apps[uid] = app
		return true
	}

	return false
}

// UpdateObservedGeneration
// This is used to track if the status is up-to-date with the spec.
func (wb *Workbench) UpdateObservedGeneration() bool {
	generation := wb.Generation
	if wb.Status.ObservedGeneration < generation {
		wb.Status.ObservedGeneration = generation
		return true
	}
	return false
}

// CleanOrphanedAppStatuses removes status entries for apps that no longer exist in spec.
// Returns true if any entries were removed.
func (wb *Workbench) CleanOrphanedAppStatuses() bool {
	if wb.Status.Apps == nil {
		return false
	}

	removed := false
	for uid := range wb.Status.Apps {
		if _, exists := wb.Spec.Apps[uid]; !exists {
			delete(wb.Status.Apps, uid)
			removed = true
		}
	}
	return removed
}
