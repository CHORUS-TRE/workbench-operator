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

// WorkbenchAppState tells which status the application is in.
//
// An app always goes from Running to Stopped or Killed if it's externally stopped or killed.
// Otherwise, the actual status is found in the /status section.
type WorkbenchAppState string

const (
	// WorkbenchAppStateRunning is used to create a running application.
	WorkbenchAppStateRunning WorkbenchAppState = "Running"

	// WorkbenchAppStateStopped is used to stop a running application.
	WorkbenchAppStateStopped WorkbenchAppState = "Stopped"

	// WorkbenchAppStateKilled is used to force kill a running application.
	WorkbenchAppStateKilled WorkbenchAppState = "Killed"
)

// WorkbenchServer defines the server configuration.
type WorkbenchServer struct {
	// Version defines the version to use.
	Version string `json:"version,omitempty"`

	// TODO: add anything you'd like to configure. E.g. resources, Xpra options, auth, etc.
}

// WorkbenchApp defines one application running in the workbench.
type WorkbenchApp struct {
	// Name is the application name (likely its OCI image name as well)
	Name string `json:"name"`
	// Version defines the version to use.
	Version string `json:"version,omitempty"`
	// State defines the desired state
	State WorkbenchAppState `json:"state,omitempty"`

	// TODO: add anything you'd like to configure. E.g. resources, (App data) volume, etc.
}

// WorkbenchSpec defines the desired state of Workbench
type WorkbenchSpec struct {
	// Server represents the configuration of the server part.
	Server WorkbenchServer `json:"server,omitempty"`
	// Apps represent a list of applications any their state
	Apps []WorkbenchApp `json:"apps,omitempty"`
}

// WorkbenchStatusAppStatus are the effective status of a launched app.
//
// It matches the Deployment Status,
// See. https://kubernetes.io/docs/concepts/workloads/controllers/deployment/#deployment-status
type WorkbenchStatusAppStatus string

const (
	// WorkbenchStatusAppStatusRunning describes a deployed app
	WorkbenchStatusAppStatusComplete WorkbenchStatusAppStatus = "Complete"

	// WorkbenchStatusAppStatusWaiting describes a pending app.
	WorkbenchStatusAppStatusProgressing WorkbenchStatusAppStatus = "Progressing"

	// WorkbenchStatusAppStatusFailed describes a failed app.
	WorkbenchStatusAppStatusFailed WorkbenchStatusAppStatus = "Failed"
)

// WorkbenchStatusServerStatus is identical to the App status.
type WorkbenchStatusServerStatus string

const (
	// WorkbenchStatusServerStatusRunning describes a deployed server
	WorkbenchStatusServerStatusComplete WorkbenchStatusServerStatus = "Complete"

	// WorkbenchStatusServerStatusWaiting describes a pending server.
	WorkbenchStatusServerStatusProgressing WorkbenchStatusServerStatus = "Progressing"

	// WorkbenchStatusServerStatusFailed describes a failed server.
	WorkbenchStatusServerStatusFailed WorkbenchStatusServerStatus = "Failed"
)

// WorkbenchStatusServer represents the server status.
type WorkbenchStatusServer struct {
	// Revision is the values of the "deployment.kubernetes.io/revision" metadata.
	Revision int `json:"revision"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusServerStatus `json:"status"`
}

// WorkbenchStatusappStatus informs about the state of the apps.
type WorkbenchStatusApp struct {
	// Revision is the values of the "deployment.kubernetes.io/revision" metadata.
	Revision int `json:"revision"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusAppStatus `json:"status"`
}

// WorkbenchStatus defines the observed state of Workbench
type WorkbenchStatus struct {
	Server WorkbenchStatusServer `json:"server"`
	Apps   []WorkbenchStatusApp  `json:"apps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.server.version`
// +kubebuilder:printcolumn:name="Apps",type=string,JSONPath=`.spec.apps[*].name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
