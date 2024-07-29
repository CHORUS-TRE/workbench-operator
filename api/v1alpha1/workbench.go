package v1alpha1

import (
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
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
