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
}

// WorkspaceStatus defines the observed state of Workspace
type WorkspaceStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// NetworkPolicy is the network policy mode currently active for this workspace.
	// Values: "Progressing" (reconcile in progress), "Open" (full internet allowed),
	// "Airgapped" (all external traffic blocked), "FQDNAllowlist" (FQDN allowlist active),
	// "Error" (policy could not be applied, see conditions for reason).
	// +optional
	// +kubebuilder:validation:Enum=Progressing;Open;Airgapped;FQDNAllowlist;Error
	NetworkPolicy string `json:"networkPolicy,omitempty"`

	// Conditions represent the latest available observations of a Workspace's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Network-Policy",type=string,JSONPath=`.status.networkPolicy`
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
