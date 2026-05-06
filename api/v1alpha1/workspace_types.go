package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type constants for Workspace status.
const (
	// ConditionNetworkPolicyReady indicates whether the CiliumNetworkPolicy
	// has been successfully reconciled for this workspace.
	ConditionNetworkPolicyReady = "NetworkPolicyReady"
)

// NetworkPolicy status values for WorkspaceStatus.NetworkPolicy.
const (
	// NetworkPolicyProgressing means the workspace was just created and the
	// network policy reconciliation has not completed yet.
	NetworkPolicyProgressing = "Progressing"

	// NetworkPolicyOpen means the policy is applied with full internet access.
	NetworkPolicyOpen = "Open"

	// NetworkPolicyAirgapped means the policy is applied with all external traffic blocked.
	NetworkPolicyAirgapped = "Airgapped"

	// NetworkPolicyFQDNAllowlist means the policy is applied with an FQDN allowlist.
	NetworkPolicyFQDNAllowlist = "FQDNAllowlist"

	// NetworkPolicyError means the policy could not be applied. See conditions for reason.
	NetworkPolicyError = "Error"
)

// Condition reason constants for Workspace status.
const (
	// ReasonProgressing means the workspace was just created and the network
	// policy reconciliation has not completed yet.
	ReasonProgressing = "Progressing"

	// ReasonApplied means the network policy was successfully applied.
	// The active mode is reflected in status.networkPolicy.
	ReasonApplied = "Applied"

	// ReasonCiliumNotInstalled means the CiliumNetworkPolicy CRD is not
	// present in the cluster, so network policies cannot be enforced.
	ReasonCiliumNotInstalled = "CiliumNotInstalled"

	// ReasonInvalidFQDN means one or more AllowedFQDNs entries are invalid.
	ReasonInvalidFQDN = "InvalidFQDN"

	// ReasonReconcileError means an unexpected error occurred during
	// network policy reconciliation.
	ReasonReconcileError = "ReconcileError"
)

// WorkspaceServiceState defines the desired lifecycle state of a workspace service.
// +kubebuilder:validation:Enum=Running;Stopped;Deleted
type WorkspaceServiceState string

const (
	// WorkspaceServiceStateRunning installs or upgrades the Helm release.
	WorkspaceServiceStateRunning WorkspaceServiceState = "Running"

	// WorkspaceServiceStateStopped uninstalls the Helm release but retains PVCs (data preserved).
	WorkspaceServiceStateStopped WorkspaceServiceState = "Stopped"

	// WorkspaceServiceStateDeleted uninstalls the Helm release and deletes PVCs (full teardown).
	// The entry remains in the CRD as a historical record.
	WorkspaceServiceStateDeleted WorkspaceServiceState = "Deleted"
)

// WorkspaceServiceChart identifies a Helm chart in an OCI registry.
// Registry and Repository are optional and will fall back to operator defaults if not specified.
type WorkspaceServiceChart struct {
	// Registry represents the hostname of the OCI registry. E.g. harbor.build.chorus-tre.local
	// +optional
	Registry string `json:"registry,omitempty"`
	// Repository contains the project and chart name. E.g. services/postgres
	// +optional
	Repository string `json:"repository,omitempty"`
	// Tag contains the chart version (semver). E.g. 1.6.1
	Tag string `json:"tag"`
}

// WorkspaceServiceCredentials configures auto-generated password injection.
type WorkspaceServiceCredentials struct {
	// SecretName is the name of the Kubernetes Secret the operator creates in the workspace namespace.
	// Passwords are stored here and never written to the CRD.
	// Supports placeholders: {{.Namespace}}, {{.ReleaseName}}.
	// Precedence: this field → chart's chorus.yaml credentials.secretName → "<release-name>-creds".
	// +optional
	SecretName string `json:"secretName,omitempty"`

	// Paths is a list of dot-notation Helm value paths for which the operator auto-generates passwords.
	// One 24-char random password is generated per entry, stored in SecretName, and injected into Helm.
	// +optional
	Paths []string `json:"paths,omitempty"`
}

// WorkspaceService defines a Helm-chart-based service running in the workspace namespace.
type WorkspaceService struct {
	// State controls the service lifecycle.
	// +optional
	// +kubebuilder:default=Running
	State WorkspaceServiceState `json:"state,omitempty"`

	// Chart identifies the Helm chart to deploy.
	// Registry and Repository are optional and fall back to operator defaults.
	Chart WorkspaceServiceChart `json:"chart"`

	// Values contains free-form Helm values merged at install/upgrade time.
	// Use to override chart defaults (e.g. storage.requestedSize).
	// Credential values take precedence over anything set here.
	// +optional
	Values *apiextensionsv1.JSON `json:"values,omitempty"`

	// Credentials configures auto-generated password injection.
	// Passwords are never written to the CRD — only to the named Secret.
	// +optional
	Credentials *WorkspaceServiceCredentials `json:"credentials,omitempty"`

	// ConnectionInfoTemplate is a string with placeholder substitution rendered into status.services[*].connectionInfo.
	// Supported placeholders (exact syntax, no spaces): {{.Namespace}}, {{.ReleaseName}}, {{.SecretName}}.
	// This is not a full Go template — conditionals and pipelines are not supported.
	// +optional
	ConnectionInfoTemplate string `json:"connectionInfoTemplate,omitempty"`

	// ComputedValues is a map of dot-notation Helm value paths to placeholder strings evaluated at deploy time.
	// Supported placeholders (exact syntax, no spaces): {{.Namespace}}, {{.ReleaseName}}, {{.SecretName}}.
	// Computed values are merged last and take precedence over Values and Credentials.
	// Example: {"mlflow.backendStore.postgres.host": "{{.ReleaseName}}-mlflow-db"}
	// +optional
	ComputedValues map[string]string `json:"computedValues,omitempty"`
}

// WorkspaceStatusServiceStatus is the observed state of a workspace service.
// +kubebuilder:validation:Enum=Progressing;Running;Stopped;Deleted;Failed
type WorkspaceStatusServiceStatus string

const (
	WorkspaceStatusServiceStatusProgressing WorkspaceStatusServiceStatus = "Progressing"
	WorkspaceStatusServiceStatusRunning     WorkspaceStatusServiceStatus = "Running"
	WorkspaceStatusServiceStatusStopped     WorkspaceStatusServiceStatus = "Stopped"
	WorkspaceStatusServiceStatusDeleted     WorkspaceStatusServiceStatus = "Deleted"
	WorkspaceStatusServiceStatusFailed      WorkspaceStatusServiceStatus = "Failed"
)

// WorkspaceStatusService is the observed status of a workspace service.
type WorkspaceStatusService struct {
	// Status is the observed lifecycle state of the service.
	Status WorkspaceStatusServiceStatus `json:"status"`

	// Message provides additional context (e.g. error details).
	// +optional
	Message string `json:"message,omitempty"`

	// ConnectionInfo is the rendered connection string from connectionInfoTemplate.
	// +optional
	ConnectionInfo string `json:"connectionInfo,omitempty"`

	// SecretName is the name of the Secret containing the service credentials.
	// +optional
	SecretName string `json:"secretName,omitempty"`
}

// WorkspaceSpec defines the desired state of Workspace
type WorkspaceSpec struct {
	// NetworkPolicy defines the desired network policy mode for this workspace.
	// - Open: all external internet traffic is allowed.
	// - Airgapped: all external traffic is blocked.
	// - FQDNAllowlist: only the FQDNs listed in AllowedFQDNs are allowed.
	// +kubebuilder:validation:Enum=Open;Airgapped;FQDNAllowlist
	NetworkPolicy string `json:"networkPolicy"`

	// AllowedFQDNs is a list of fully qualified domain names that are permitted in this workspace.
	// Only used when NetworkPolicy is FQDNAllowlist. Each entry can be an exact domain (e.g. example.com)
	// or a wildcard pattern (e.g. *.corp.internal).
	//
	// Note: wildcards are opt-in. Specifying "example.com" does not implicitly allow "*.example.com".
	// Note: entries must contain at least one dot (two labels), e.g. "example.com".
	// Note: each entry produces one Cilium FQDN selector. To allow both apex and subdomains,
	// add both "example.com" and "*.example.com" as separate entries (counts toward MaxItems).
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=253
	// +listType=set
	AllowedFQDNs []string `json:"allowedFQDNs,omitempty"`

	// Services represent a map of services and their state
	// +optional
	Services map[string]WorkspaceService `json:"services,omitempty"`
}

// NetworkPolicyStatus is the observed state of the workspace network policy.
type NetworkPolicyStatus struct {
	// Status is the network policy reconciliation status.
	// Values: "Progressing", "Open", "Airgapped", "FQDNAllowlist", "Error".
	// +optional
	// +kubebuilder:validation:Enum=Progressing;Open;Airgapped;FQDNAllowlist;Error
	Status string `json:"status,omitempty"`

	// Message is a human-readable description of the active policy or the reason for an error.
	// +optional
	Message string `json:"message,omitempty"`
}

// WorkspaceStatus defines the observed state of Workspace
type WorkspaceStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NetworkPolicy is the observed state of the network policy for this workspace.
	// +optional
	NetworkPolicy NetworkPolicyStatus `json:"networkPolicy,omitempty"`

	// Conditions represent the latest available observations of a Workspace's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Services represent the observed state of each workspace service.
	// +optional
	Services map[string]WorkspaceStatusService `json:"services,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Network-Policy",type=string,JSONPath=`.status.networkPolicy.status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="NetworkPolicyReady")].reason`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is the Schema for the workspaces API
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec,omitempty"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}
