package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceSpec defines the desired state of Workspace
type WorkspaceSpec struct {
	// Airgapped indicates whether this workspace operates in an airgapped environment
	Airgapped bool `json:"airgapped"`

	// AllowedFQDNs is a list of fully qualified domain names that are permitted in this workspace
	// +optional
	AllowedFQDNs []string `json:"allowedFQDNs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:printcolumn:name="Airgapped",type=boolean,JSONPath=`.spec.airgapped`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Workspace is the Schema for the workspaces API
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec WorkspaceSpec `json:"spec,omitempty"`
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
