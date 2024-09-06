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

	// Wallpaper lets you define a wallpaper to download (at first run) and use.
	Wallpaper string `json:"wallpaper,omitempty"`
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
	// Service Account to be used by the pods.
	ServiceAccount string `json:"serviceAccountName,omitempty"`
}

// WorkbenchStatusAppStatus are the effective status of a launched app.
//
// It matches the Job Status,
// See https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/job-v1/#JobStatus
type WorkbenchStatusAppStatus string

const (
	// WorkbenchStatusAppStatusRunning describes a up and running app.
	WorkbenchStatusAppStatusRunning WorkbenchStatusAppStatus = "Running"

	// WorkbenchStatusAppStatusComplete describes a terminated app.
	WorkbenchStatusAppStatusComplete WorkbenchStatusAppStatus = "Complete"

	// WorkbenchStatusAppStatusProgressing describes a pending app.
	WorkbenchStatusAppStatusProgressing WorkbenchStatusAppStatus = "Progressing"

	// WorkbenchStatusAppStatusFailed describes a failed app.
	WorkbenchStatusAppStatusFailed WorkbenchStatusAppStatus = "Failed"
)

// WorkbenchStatusServerStatus is identical to the App status.
type WorkbenchStatusServerStatus string

const (
	// WorkbenchStatusServerStatusRunning describes a deployed server
	WorkbenchStatusServerStatusRunning WorkbenchStatusServerStatus = "Running"

	// WorkbenchStatusServerStatusProgressing describes a pending server.
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
