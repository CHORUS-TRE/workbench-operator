package v1alpha1

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helpers

func newWB() *Workbench {
	return &Workbench{}
}

func int32p(v int32) *int32 { return &v }
func boolp(v bool) *bool    { return &v }

// ── UpdateStatusFromDeployment ───────────────────────────────────────────────

func TestUpdateStatusFromDeployment_NoAnnotation(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Available", Status: corev1.ConditionTrue},
	}
	updated := wb.UpdateStatusFromDeployment(dep)
	if !updated {
		t.Fatal("expected updated=true")
	}
	if wb.Status.ServerDeployment.Revision != -1 {
		t.Fatalf("expected revision -1, got %d", wb.Status.ServerDeployment.Revision)
	}
	if wb.Status.ServerDeployment.Status != "Running" {
		t.Fatalf("expected Running, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_WithRevision(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "3"},
		},
	}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Available", Status: corev1.ConditionTrue},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Revision != 3 {
		t.Fatalf("expected revision 3, got %d", wb.Status.ServerDeployment.Revision)
	}
}

func TestUpdateStatusFromDeployment_InvalidRevision(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "notanumber"},
		},
	}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Available"},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Revision != -1 {
		t.Fatalf("expected -1 for invalid annotation, got %d", wb.Status.ServerDeployment.Revision)
	}
}

func TestUpdateStatusFromDeployment_NoConditions(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	updated := wb.UpdateStatusFromDeployment(dep)
	if !updated {
		t.Fatal("expected updated=true when no conditions")
	}
	if wb.Status.ServerDeployment.Status != WorkbenchStatusServerStatusProgressing {
		t.Fatalf("expected Progressing, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_ProgressingTrue_NewReplicaSet(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Progressing", Status: corev1.ConditionTrue, Reason: "NewReplicaSetAvailable"},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Status != "Running" {
		t.Fatalf("expected Running, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_ProgressingTrue_Other(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Progressing", Status: corev1.ConditionTrue, Reason: "ReplicaSetUpdated"},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Status != "Progressing" {
		t.Fatalf("expected Progressing, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_ProgressingFalse(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Progressing", Status: corev1.ConditionFalse},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Status != "Failed" {
		t.Fatalf("expected Failed, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_ReplicaFailure(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "ReplicaFailure"},
	}
	// ReplicaFailure has no-op body; status stays at zero value → no change
	updated := wb.UpdateStatusFromDeployment(dep)
	_ = updated // implementation-defined; just ensure it doesn't panic
}

func TestUpdateStatusFromDeployment_UnknownConditionType(t *testing.T) {
	wb := newWB()
	dep := appsv1.Deployment{}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "SomeUnknownType"},
	}
	wb.UpdateStatusFromDeployment(dep)
	if wb.Status.ServerDeployment.Status != "Failed" {
		t.Fatalf("expected Failed for unknown condition type, got %s", wb.Status.ServerDeployment.Status)
	}
}

func TestUpdateStatusFromDeployment_NoChangeIdempotent(t *testing.T) {
	wb := newWB()
	wb.Status.ServerDeployment.Status = "Running"
	wb.Status.ServerDeployment.Revision = 1
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "1"},
		},
	}
	dep.Status.Conditions = []appsv1.DeploymentCondition{
		{Type: "Available", Status: corev1.ConditionTrue},
	}
	updated := wb.UpdateStatusFromDeployment(dep)
	if updated {
		t.Fatal("expected updated=false when nothing changed")
	}
}

// ── UpdateStatusFromJob ──────────────────────────────────────────────────────

func TestUpdateStatusFromJob_ActiveAndReady(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	job := batchv1.Job{Status: batchv1.JobStatus{Active: 1, Ready: int32p(1)}}
	updated := wb.UpdateStatusFromJob("uid1", job, "running")
	if !updated {
		t.Fatal("expected updated=true")
	}
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusRunning {
		t.Fatalf("expected Running, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_ActiveNotReady(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	job := batchv1.Job{Status: batchv1.JobStatus{Active: 1, Ready: int32p(0)}}
	wb.UpdateStatusFromJob("uid1", job, "starting")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusProgressing {
		t.Fatalf("expected Progressing, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_Succeeded(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	job := batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}
	wb.UpdateStatusFromJob("uid1", job, "done")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusComplete {
		t.Fatalf("expected Complete, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_Failed(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	job := batchv1.Job{Status: batchv1.JobStatus{Failed: 1}}
	wb.UpdateStatusFromJob("uid1", job, "Job failed")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusFailed {
		t.Fatalf("expected Failed, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_NoPodsProgressing(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	job := batchv1.Job{} // all counters zero
	wb.UpdateStatusFromJob("uid1", job, "Job starting")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusProgressing {
		t.Fatalf("expected Progressing, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedStopped(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateStopped}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 0},
	}
	wb.UpdateStatusFromJob("uid1", job, "stopping")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusStopped {
		t.Fatalf("expected Stopped, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedStopping(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateStopped}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 1},
	}
	wb.UpdateStatusFromJob("uid1", job, "stopping")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusStopping {
		t.Fatalf("expected Stopping, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedKilled(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateKilled}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 0},
	}
	wb.UpdateStatusFromJob("uid1", job, "killed")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusKilled {
		t.Fatalf("expected Killed, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedKilling(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateKilled}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 1},
	}
	wb.UpdateStatusFromJob("uid1", job, "killing")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusKilling {
		t.Fatalf("expected Killing, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedNoMatchingState(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateRunning}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 1},
	}
	wb.UpdateStatusFromJob("uid1", job, "msg")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusProgressing {
		t.Fatalf("expected Progressing, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_SuspendedNoMatchingState_NoActive(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {State: WorkbenchAppStateRunning}}
	job := batchv1.Job{
		Spec:   batchv1.JobSpec{Suspend: boolp(true)},
		Status: batchv1.JobStatus{Active: 0},
	}
	wb.UpdateStatusFromJob("uid1", job, "msg")
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusComplete {
		t.Fatalf("expected Complete, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_PreservesDetailedFailureMessage(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	// First call: set a detailed failure message
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusFailed, Message: "OOMKilled: container exceeded memory limit"},
	}
	job := batchv1.Job{Status: batchv1.JobStatus{Failed: 1}}
	wb.UpdateStatusFromJob("uid1", job, "Job failed")
	if wb.Status.Apps["uid1"].Message != "OOMKilled: container exceeded memory limit" {
		t.Fatalf("expected detailed message preserved, got %q", wb.Status.Apps["uid1"].Message)
	}
}

func TestUpdateStatusFromJob_DoesNotPreserveTransitionalMessage(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusFailed, Message: "Job starting"},
	}
	job := batchv1.Job{Status: batchv1.JobStatus{Failed: 1}}
	wb.UpdateStatusFromJob("uid1", job, "Job failed")
	if wb.Status.Apps["uid1"].Message != "Job failed" {
		t.Fatalf("expected generic message, got %q", wb.Status.Apps["uid1"].Message)
	}
}

func TestUpdateStatusFromJob_UIDNotInSpec(t *testing.T) {
	wb := newWB()
	// uid1 NOT in Spec.Apps — app is initialised then job status applied in same call
	job := batchv1.Job{Status: batchv1.JobStatus{Active: 1, Ready: int32p(1)}}
	updated := wb.UpdateStatusFromJob("uid1", job, "msg")
	if !updated {
		t.Fatal("expected updated=true for new uid")
	}
	// Initialized with Unknown, then job status (Active+Ready) overrides to Running
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusRunning {
		t.Fatalf("expected Running after active+ready job, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestUpdateStatusFromJob_NoChange(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusRunning, Message: "running"},
	}
	job := batchv1.Job{Status: batchv1.JobStatus{Active: 1, Ready: int32p(1)}}
	updated := wb.UpdateStatusFromJob("uid1", job, "running")
	if updated {
		t.Fatal("expected updated=false when nothing changed")
	}
}

// ── SetAppStatusFailed ───────────────────────────────────────────────────────

func TestSetAppStatusFailed_NewApp(t *testing.T) {
	wb := newWB()
	updated := wb.SetAppStatusFailed("uid1", "image pull failed")
	if !updated {
		t.Fatal("expected updated=true for new app")
	}
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusFailed {
		t.Fatalf("expected Failed, got %s", wb.Status.Apps["uid1"].Status)
	}
	if wb.Status.Apps["uid1"].Message != "image pull failed" {
		t.Fatalf("unexpected message: %s", wb.Status.Apps["uid1"].Message)
	}
}

func TestSetAppStatusFailed_ExistingApp(t *testing.T) {
	wb := newWB()
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusRunning, Message: "old"},
	}
	updated := wb.SetAppStatusFailed("uid1", "new error")
	if !updated {
		t.Fatal("expected updated=true")
	}
	if wb.Status.Apps["uid1"].Status != WorkbenchStatusAppStatusFailed {
		t.Fatalf("expected Failed, got %s", wb.Status.Apps["uid1"].Status)
	}
}

func TestSetAppStatusFailed_NoChangeIdempotent(t *testing.T) {
	wb := newWB()
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusFailed, Message: "same error"},
	}
	updated := wb.SetAppStatusFailed("uid1", "same error")
	if updated {
		t.Fatal("expected updated=false when already failed with same message")
	}
}

// ── UpdateObservedGeneration ────────────────────────────────────────────────

func TestUpdateObservedGeneration_Increments(t *testing.T) {
	wb := newWB()
	wb.Generation = 5
	wb.Status.ObservedGeneration = 3
	updated := wb.UpdateObservedGeneration()
	if !updated {
		t.Fatal("expected updated=true")
	}
	if wb.Status.ObservedGeneration != 5 {
		t.Fatalf("expected 5, got %d", wb.Status.ObservedGeneration)
	}
}

func TestUpdateObservedGeneration_AlreadyCurrent(t *testing.T) {
	wb := newWB()
	wb.Generation = 5
	wb.Status.ObservedGeneration = 5
	updated := wb.UpdateObservedGeneration()
	if updated {
		t.Fatal("expected updated=false when already current")
	}
}

// ── CleanOrphanedAppStatuses ─────────────────────────────────────────────────

func TestCleanOrphanedAppStatuses_NilStatus(t *testing.T) {
	wb := newWB()
	updated := wb.CleanOrphanedAppStatuses()
	if updated {
		t.Fatal("expected updated=false for nil status")
	}
}

func TestCleanOrphanedAppStatuses_RemovesOrphan(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1":    {Status: WorkbenchStatusAppStatusRunning},
		"orphan1": {Status: WorkbenchStatusAppStatusFailed},
	}
	updated := wb.CleanOrphanedAppStatuses()
	if !updated {
		t.Fatal("expected updated=true")
	}
	if _, exists := wb.Status.Apps["orphan1"]; exists {
		t.Fatal("orphan should have been removed")
	}
	if _, exists := wb.Status.Apps["uid1"]; !exists {
		t.Fatal("uid1 should remain")
	}
}

func TestCleanOrphanedAppStatuses_NoOrphans(t *testing.T) {
	wb := newWB()
	wb.Spec.Apps = map[string]WorkbenchApp{"uid1": {}}
	wb.Status.Apps = map[string]WorkbenchStatusApp{
		"uid1": {Status: WorkbenchStatusAppStatusRunning},
	}
	updated := wb.CleanOrphanedAppStatuses()
	if updated {
		t.Fatal("expected updated=false when no orphans")
	}
}
