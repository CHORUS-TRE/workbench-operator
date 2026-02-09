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

// ClipboardDirection defines the clipboard direction between workbench and local machine.
// +kubebuilder:validation:Enum=disabled;to-server;to-client;both
type ClipboardDirection string

const (
	// ClipboardDisabled disables clipboard (default)
	ClipboardDisabled ClipboardDirection = "disabled"
	// ClipboardToServer allows paste from host to container only
	ClipboardToServer ClipboardDirection = "to-server"
	// ClipboardToClient allows copy from container to host only
	ClipboardToClient ClipboardDirection = "to-client"
	// ClipboardBoth allows bidirectional clipboard
	ClipboardBoth ClipboardDirection = "both"
)

// WorkbenchServer defines the server configuration.
type WorkbenchServer struct {
	// Version defines the version to use for the xpra server.
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

	// Clipboard defines the clipboard direction between the workbench and local machine.
	// Options: disabled (default), to-server, to-client, both
	// +optional
	// +kubebuilder:default=disabled
	Clipboard ClipboardDirection `json:"clipboard,omitempty"`

	// Resources describes the compute resource requirements for the server container.
	// When specified, overrides operator-level defaults.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// Image represents the configuration of a custom image for an app.
type Image struct {
	// Registry represents the hostname of the registry. E.g. quay.io
	// +optional
	Registry string `json:"registry,omitempty"`
	// Repository contains the image name. E.g. apps/myapp
	// +optional
	Repository string `json:"repository,omitempty"`
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
	// JWTURL is the URL to refresh the short lived JWT token
	// +optional
	// +kubebuilder:validation:Pattern=`^https://.*`
	JWTURL *string `json:"jwtUrl,omitempty"`
	// JWTToken is a short lived jwt used to authenticate to the JWTURL
	// +optional
	JWTToken *string `json:"jwtToken,omitempty"`
}

// InitContainerConfig defines the init container configuration.
type InitContainerConfig struct {
	// Version defines the version to use for the app-init container.
	// +optional
	// +default:value="latest"
	Version string `json:"version,omitempty"`
}

// StorageConfig defines storage mount configuration
type StorageConfig struct {
	// S3 enables S3 storage mounting via JuiceFS at /mnt/workspace-archive
	// The init container creates a symlink from /home/{user}/workspace-archive to this mount point
	// +optional
	// +default:value=true
	S3 bool `json:"s3,omitempty"`

	// NFS enables NFS storage mounting at /mnt/workspace-scratch
	// The init container creates a symlink from /home/{user}/workspace-scratch to this mount point
	// +optional
	// +default:value=true
	NFS bool `json:"nfs,omitempty"`

	// Local enables local storage mounting for development at /mnt/workspace-local
	// The init container creates a symlink from /home/{user}/workspace-local to this mount point
	// This is only available when the operator is started with --local-storage-enabled flag
	// +optional
	// +default:value=false
	Local bool `json:"local,omitempty"`
}

// WorkbenchApp defines one application running in the workbench.
type WorkbenchApp struct {
	// Name is the application name (likely its OCI image name as well)
	// +kubebuilder:validation:MinLength:=1
	// +kubebuilder:validation:MaxLength:=30
	// +kubebuilder:validation:Pattern:="[a-zA-Z0-9_][a-zA-Z0-9_\\-\\.]*"
	Name string `json:"name"`

	// State defines the desired state
	// Valid values are:
	// - "Running" (default): application is running
	// - "Stopped": application has been stopped
	// - "Killed": application has been force stopped
	// +optional
	// +default:value="Running"
	State WorkbenchAppState `json:"state,omitempty"`

	// Image specifies the container image for this app.
	// Registry and Repository are optional and will fall back to operator defaults if not specified.
	// Tag is required.
	Image Image `json:"image"`

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
	// InitContainer represents the configuration of the init container.
	// +optional
	InitContainer *InitContainerConfig `json:"initContainer,omitempty"`
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
// +kubebuilder:validation:Enum=Unknown;Running;Complete;Progressing;Failed;Stopping;Stopped;Killing;Killed
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

	// WorkbenchStatusAppStatusStopping describes an app that is being stopped (pod terminating).
	WorkbenchStatusAppStatusStopping WorkbenchStatusAppStatus = "Stopping"

	// WorkbenchStatusAppStatusStopped describes an app that was stopped.
	WorkbenchStatusAppStatusStopped WorkbenchStatusAppStatus = "Stopped"

	// WorkbenchStatusAppStatusKilling describes an app that is being force killed (pod terminating).
	WorkbenchStatusAppStatusKilling WorkbenchStatusAppStatus = "Killing"

	// WorkbenchStatusAppStatusKilled describes an app that was force killed.
	WorkbenchStatusAppStatusKilled WorkbenchStatusAppStatus = "Killed"
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

// ServerPodStatus represents the health status of the server pod.
// +kubebuilder:validation:Enum=Waiting;Starting;Ready;Failing;Restarting;Terminating;Terminated;Unknown
type ServerPodStatus string

const (
	// ServerPodStatusWaiting describes a pod that hasn't started
	ServerPodStatusWaiting ServerPodStatus = "Waiting"

	// ServerPodStatusStarting describes a pod that is starting up
	ServerPodStatusStarting ServerPodStatus = "Starting"

	// ServerPodStatusReady describes a healthy pod
	ServerPodStatusReady ServerPodStatus = "Ready"

	// ServerPodStatusFailing describes a pod failing health checks
	ServerPodStatusFailing ServerPodStatus = "Failing"

	// ServerPodStatusRestarting describes a pod that has restarted recently
	ServerPodStatusRestarting ServerPodStatus = "Restarting"

	// ServerPodStatusTerminating describes a pod being shut down
	ServerPodStatusTerminating ServerPodStatus = "Terminating"

	// ServerPodStatusTerminated describes a stopped/crashed pod
	ServerPodStatusTerminated ServerPodStatus = "Terminated"

	// ServerPodStatusUnknown describes a pod in unknown state
	ServerPodStatusUnknown ServerPodStatus = "Unknown"
)

// ServerPodHealth provides health information for the server pod.
type ServerPodHealth struct {
	// Status represents the current status of the server pod
	Status ServerPodStatus `json:"status"`

	// Ready indicates if all containers are ready
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

	// ServerPod provides health information for the server pod
	// +optional
	ServerPod *ServerPodHealth `json:"serverPod,omitempty"`
}

// WorkbenchStatusappStatus informs about the state of the apps.
type WorkbenchStatusApp struct {
	// Revision is the values of the "deployment.kubernetes.io/revision" metadata.
	Revision int `json:"revision"`

	// Status informs about the real state of the app.
	Status WorkbenchStatusAppStatus `json:"status"`

	// Message provides additional context about the status
	// +optional
	Message string `json:"message,omitempty"`
}

// WorkbenchStatus defines the observed state of Workbench
type WorkbenchStatus struct {
	ObservedGeneration int64                         `json:"observedGeneration"`
	ServerDeployment   WorkbenchStatusServer         `json:"serverDeployment"`
	Apps               map[string]WorkbenchStatusApp `json:"apps,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.server.version`
// +kubebuilder:printcolumn:name="Server-Health",type=string,JSONPath=`.status.serverDeployment.serverPod.status`
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
