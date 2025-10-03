package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// Important: Run "make" to regenerate code after modifying this file

// WorkbenchAppState tells which status the application is in.
//
// An app always goes from Running to Stopped or Killed if it's externally stopped or killed.
// Otherwise, the actual status is found in the /status section.
// +kubebuilder:validation:Enum=Running;Stopped;Killed
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
	// +optional
	// +default:value="latest"
	Version string `json:"version,omitempty"`

	// TODO: add anything you'd like to configure. E.g. resources, Xpra options, auth, etc.
	// InitialResolutionWidth defines the initial resolution width of the Xpra server.
	// +optional
	InitialResolutionWidth int `json:"initialResolutionWidth,omitempty"`
	// InitialResolutionHeight defines the initial resolution height of the Xpra server.
	// +optional
	InitialResolutionHeight int `json:"initialResolutionHeight,omitempty"`
	// User defines the username for the workbench server.
	// +optional
	// +kubebuilder:default="chorus"
	User string `json:"user,omitempty"`

	// UserID defines the user ID for the workbench server.
	// +optional
	// +kubebuilder:default=1001
	// +kubebuilder:validation:Minimum=1001
	UserID int `json:"userid,omitempty"`
}

// Image represents the configuration of a custom image for an app.
type Image struct {
	// Registry represents the hostname of the registry. E.g. quay.io
	Registry string `json:"registry"`
	// Repository contains the image name. E.g. apps/myapp
	Repository string `json:"repository"`
	// Tag contains the version identifier.
	// +optional
	// +default:value="latest"
	// +kubebuilder:validation:Pattern:="[a-zA-Z0-9_][a-zA-Z0-9_\\-\\.]*"
	Tag string `json:"tag,omitempty"`
}

// KioskConfig defines configuration specific to kiosk mode applications
type KioskConfig struct {
	// URL to load in the kiosk browser
	// +kubebuilder:validation:Pattern=`^https://.*`
	URL string `json:"url"`
}

// StorageConfig defines storage mount configuration
type StorageConfig struct {
	// S3 enables S3 storage mounting via JuiceFS at /home/{user}/workspace-archive
	// +optional
	// +default:value=true
	S3 bool `json:"s3,omitempty"`

	// NFS enables NFS storage mounting at /home/{user}/workspace-scratch
	// +optional
	// +default:value=true
	NFS bool `json:"nfs,omitempty"`
}

// WorkbenchApp defines one application running in the workbench.
type WorkbenchApp struct {
	// Name is the application name (likely its OCI image name as well)
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=30
	// +kubebuilder:validation:Pattern:="[a-zA-Z0-9_][a-zA-Z0-9_\\-\\.]*"
	Name string `json:"name"`

	// Version defines the version to use.
	// +optional
	// +default:value="latest"
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=128
	// +kubebuilder:validation:Pattern:="[a-zA-Z0-9_][a-zA-Z0-9_\\-\\.]*"
	Version string `json:"version,omitempty"`

	// State defines the desired state
	// Valid values are:
	// - "Running" (default): application is running
	// - "Stopped": application has been stopped
	// - "Killed": application has been force stopped
	// +optional
	// +default:value="Running"
	State WorkbenchAppState `json:"state,omitempty"`

	// Image overwrites the default image built using the default registry, name, and version.
	Image Image `json:"image,omitempty"`

	// ShmSize defines the size of the required extra /dev/shm space.
	// +optional
	ShmSize *resource.Quantity `json:"shmSize,omitempty"`

	// Resources describes the compute resource requirements.
	// +optional
	// Add anything you'd like to configure. E.g. resources, (App data) volume, etc.
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// KioskConfig holds kiosk-specific configuration
	// +optional
	KioskConfig *KioskConfig `json:"kioskConfig,omitempty"`
}

// WorkbenchSpec defines the desired state of Workbench
type WorkbenchSpec struct {
	// Server represents the configuration of the server part.
	// +optional
	Server WorkbenchServer `json:"server,omitempty"`
	// Apps represent a map of applications any their state
	// +optional
	Apps map[string]WorkbenchApp `json:"apps,omitempty"`
	// Service Account to be used by the pods.
	// +optional
	// +default:value="default"
	ServiceAccount string `json:"serviceAccountName,omitempty"`
	// ImagePullSecrets is the secret(s) needed to pull the image(s).
	// +optional
	// +kubebuilder:validation:items:MinLength:=1
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`
	// Storage defines storage mount configuration for S3 and NFS
	// +optional
	Storage *StorageConfig `json:"storage,omitempty"`
}

// WorkbenchStatusAppStatus are the effective status of a launched app.
//
// It matches the Job Status,
// See https://kubernetes.io/docs/reference/kubernetes-api/workload-resources/job-v1/#JobStatus
// +kubebuilder:validation:Enum=Unknown;Running;Complete;Progressing;Failed
type WorkbenchStatusAppStatus string

const (
	// WorkbenchStatuAppStatusUnknown describe a non-existing app.
	WorkbenchStatusAppStatusUnknown WorkbenchStatusAppStatus = "Unknown"

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
// +kubebuilder:validation:Enum=Running;Progressing;Failed
type WorkbenchStatusServerStatus string

const (
	// WorkbenchStatusServerStatusRunning describes a deployed server
	WorkbenchStatusServerStatusRunning WorkbenchStatusServerStatus = "Running"

	// WorkbenchStatusServerStatusProgressing describes a pending server.
	WorkbenchStatusServerStatusProgressing WorkbenchStatusServerStatus = "Progressing"

	// WorkbenchStatusServerStatusFailed describes a failed server.
	WorkbenchStatusServerStatusFailed WorkbenchStatusServerStatus = "Failed"
)

// ServerContainerStatus represents the health status of the server container.
// +kubebuilder:validation:Enum=Waiting;Starting;Ready;Failing;Restarting;Terminating;Terminated;Unknown
type ServerContainerStatus string

const (
	// ServerContainerStatusWaiting describes a container that hasn't started
	ServerContainerStatusWaiting ServerContainerStatus = "Waiting"

	// ServerContainerStatusStarting describes a container that is starting up
	ServerContainerStatusStarting ServerContainerStatus = "Starting"

	// ServerContainerStatusReady describes a healthy container
	ServerContainerStatusReady ServerContainerStatus = "Ready"

	// ServerContainerStatusFailing describes a container failing health checks
	ServerContainerStatusFailing ServerContainerStatus = "Failing"

	// ServerContainerStatusRestarting describes a container that has restarted recently
	ServerContainerStatusRestarting ServerContainerStatus = "Restarting"

	// ServerContainerStatusTerminating describes a container being shut down
	ServerContainerStatusTerminating ServerContainerStatus = "Terminating"

	// ServerContainerStatusTerminated describes a stopped/crashed container
	ServerContainerStatusTerminated ServerContainerStatus = "Terminated"

	// ServerContainerStatusUnknown describes a container in unknown state
	ServerContainerStatusUnknown ServerContainerStatus = "Unknown"
)

// ServerContainerHealth provides health information for the server container.
type ServerContainerHealth struct {
	// Status represents the current status of the server container
	Status ServerContainerStatus `json:"status"`

	// Ready indicates if the readiness probe is passing
	Ready bool `json:"ready"`

	// RestartCount shows container restart count
	RestartCount int32 `json:"restartCount"`

	// Message provides additional context about the status
	// +optional
	Message string `json:"message,omitempty"`
}

// WorkbenchStatusServer represents the server status.
type WorkbenchStatusServer struct {
	// Revision is the values of the "deployment.kubernetes.io/revision" metadata.
	Revision int `json:"revision"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusServerStatus `json:"status"`

	// ServerContainer provides health information for the server container
	// +optional
	ServerContainer *ServerContainerHealth `json:"serverContainer,omitempty"`
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
	ServerDeployment WorkbenchStatusServer         `json:"serverDeployment"`
	Apps             map[string]WorkbenchStatusApp `json:"apps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.server.version`
// +kubebuilder:printcolumn:name="Apps",type=string,JSONPath=`.spec.apps[*].name`
// +kubebuilder:printcolumn:name="Server-Health",type=string,JSONPath=`.status.serverDeployment.serverContainer.status`
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
