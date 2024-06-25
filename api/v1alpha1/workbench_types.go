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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Important: Run "make" to regenerate code after modifying this file

// WorkbenchAppStatus tells which status the application is in.
type WorkbenchAppStatus string

// WorkbenchAppStatusRunning is used to create a running application.
var WorkbenchAppStatusRunning = "Running"

// WorkbenchAppStatusStopped is used to stop a running application.
var WorkbenchAppStatusStopped = "Stopped"

// WorkbenchAppStatusKilled is used to force kill a running application.
var WorkbenchAppStatusKilled = "Killed"

// WorkbenchApp defines one application running in the workbench.
type WorkbenchApp struct {
	// Name is the application name (likely its OCI image name as well)
	Name string `json:"name"`
	// Version defines the version to use.
	Version string `json:"version,omitempty"`
	// Status defines the external set status for the application
	Status WorkbenchAppStatus `json:"status,omitempty"`
}

// WorkbenchSpec defines the desired state of Workbench
type WorkbenchSpec struct {
	// ServerVersion defineswith Xpra server to run.
	ServerVersion string `json:"serverVersion,omitempty"`
	// Apps represent a list of applications any their state
	Apps []WorkbenchApp `json:"apps,omitempty"`
}

// WorkbenchStatusAppStatus are the effective status of a launched app.
//
// It matches the PodStatus.
// See. https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/#pod-and-container-status
type WorkbenchStatusAppStatus string

// WorkbenchStatusAppStatusRunning describes a running app.
var WorkbenchStatusAppStatusRunning = "Running"

// WorkbenchStatusAppStatusWaiting describes a pending app.
var WorkbenchStatusAppStatusPending = "Pending"

// WorkbenchStatusAppStatusSucceeded describes a terminated app (no errors).
var WorkbenchStatusAppStatusSucceeded = "Succeeded"

// WorkbenchStatusAppStatusFailed describes a terminated app with errors.
var WorkbenchStatusAppStatusFailed = "Failed"

// WorkbenchStatusAppStatusUnknown describes the known unknown.
var WorkbenchStatusAppStatusUnknown = "Unknown"

// WorkbenchStatusServerStatus is identical to the App status.
type WorkbenchStatusServerStatus WorkbenchStatusAppStatus

// WorkbenchStatusServer represents the server status.
type WorkbenchStatusServer struct {
	// PodName is the name of the pod
	PodName string `json:"podName"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusServerStatus `json:"status"`
}

// WorkbenchStatusappStatus informs about the state of the apps.
type WorkbenchStatusApp struct {
	// PodName is the name of the pod
	PodName string `json:"podName"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusAppStatus `json:"status"`
}

// WorkbenchStatus defines the observed state of Workbench
type WorkbenchStatus struct {
	Server WorkbenchStatusServer `json:"server"`
	Apps   []WorkbenchStatusApp  `json:"apps"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Workbench is the Schema for the workbenches API
type Workbench struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkbenchSpec   `json:"spec,omitempty"`
	Status WorkbenchStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkbenchList contains a list of Workbench
type WorkbenchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workbench `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workbench{}, &WorkbenchList{})
}
