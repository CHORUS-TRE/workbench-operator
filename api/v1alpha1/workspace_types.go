package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition type constants for Workspace status.
const (
	// ConditionNetworkPolicyReady indicates whether the CiliumNetworkPolicy
	// has been successfully reconciled for this workspace.
	ConditionNetworkPolicyReady = "NetworkPolicyReady"
)

// Condition reason constants for Workspace status.
const (
	// ReasonReconciled means the network policy was successfully applied.
	ReasonReconciled = "Reconciled"

	// ReasonCiliumNotInstalled means the CiliumNetworkPolicy CRD is not
	// present in the cluster, so network policies cannot be enforced.
	ReasonCiliumNotInstalled = "CiliumNotInstalled"

	// ReasonInvalidFQDN means one or more AllowedFQDNs entries are invalid.
	ReasonInvalidFQDN = "InvalidFQDN"

	// ReasonReconcileError means an unexpected error occurred during
	// network policy reconciliation.
	ReasonReconcileError = "ReconcileError"
)

// WorkspaceSpec defines the desired state of Workspace
type WorkspaceSpec struct {
	// Airgapped indicates whether this workspace operates in an airgapped environment
	Airgapped bool `json:"airgapped"`

	// AllowedFQDNs is a list of fully qualified domain names that are permitted in this workspace.
	// Only used when Airgapped is false. Each entry can be an exact domain (e.g. example.com)
	// or a wildcard pattern (e.g. *.corp.internal).
	// +optional
	// +kubebuilder:validation:MaxItems=64
	AllowedFQDNs []string `json:"allowedFQDNs,omitempty"`
}

// WorkspaceStatus defines the observed state of Workspace
type WorkspaceStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of a Workspace's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Airgapped",type=boolean,JSONPath=`.spec.airgapped`
// +kubebuilder:printcolumn:name="Network-Policy",type=string,JSONPath=`.status.conditions[?(@.type=="NetworkPolicyReady")].status`
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
