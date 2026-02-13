package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

var _ = Describe("WorkspaceReconciler", func() {
	const workspaceName = "test-workspace"
	const workspaceNamespace = "default"

	ctx := context.Background()

	namespacedName := types.NamespacedName{
		Name:      workspaceName,
		Namespace: workspaceNamespace,
	}

	cnpGVK := schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumNetworkPolicy",
	}

	newReconciler := func() *WorkspaceReconciler {
		return &WorkspaceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
		}
	}

	getCNP := func(name, namespace string) (*unstructured.Unstructured, error) {
		cnp := &unstructured.Unstructured{}
		cnp.SetGroupVersionKind(cnpGVK)
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, cnp)
		return cnp, err
	}

	AfterEach(func() {
		// Clean up workspace
		workspace := &defaultv1alpha1.Workspace{}
		err := k8sClient.Get(ctx, namespacedName, workspace)
		if err == nil {
			_ = k8sClient.Delete(ctx, workspace)
		}
		// Clean up any CNP that may exist
		cnp := &unstructured.Unstructured{}
		cnp.SetGroupVersionKind(cnpGVK)
		cnp.SetName(workspaceName + "-egress")
		cnp.SetNamespace(workspaceNamespace)
		_ = k8sClient.Delete(ctx, cnp)
	})

	Describe("Reconcile", func() {
		It("handles non-existent workspace gracefully", func() {
			reconciler := newReconciler()
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})

		It("sets NetworkPolicyReady=False with InvalidFQDN for invalid entries", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped:    false,
					AllowedFQDNs: []string{"valid.com", "not a domain!"},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonInvalidFQDN))
			Expect(cond.Message).To(ContainSubstring("invalid FQDN"))

			// CNP should not be created when FQDNs are invalid
			_, err = getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).To(HaveOccurred())
		})

		It("sets NetworkPolicyReady=False for non-airgapped workspace with empty FQDN entry", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped:    false,
					AllowedFQDNs: []string{""},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonInvalidFQDN))
		})
	})

	Describe("Reconcile – CNP creation (happy path)", func() {
		It("creates CNP and sets NetworkPolicyReady=True for airgapped workspace", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify condition
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonReconciled))

			// Verify CNP was created
			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetLabels()).To(HaveKeyWithValue("workspace", workspaceName))

			// Verify egress rules: airgapped → 2 rules (DNS + intra-namespace)
			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(2))
		})

		It("creates CNP with FQDN allowlist for non-airgapped workspace", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped:    false,
					AllowedFQDNs: []string{"example.com", "*.corp.internal"},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify CNP was created with 3 egress rules
			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(3)) // DNS + intra-ns + FQDN

			// Verify condition
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("creates CNP with full internet access when non-airgapped and no FQDNs", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: false,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify CNP was created with 3 egress rules
			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(3)) // DNS + intra-ns + CIDR

			// Verify the toCIDR rule is present
			lastRule := egress[2].(map[string]any)
			Expect(lastRule).To(HaveKey("toCIDR"))

			// Verify condition
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("updates ObservedGeneration on successful reconcile", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			Expect(updated.Status.ObservedGeneration).To(Equal(updated.Generation))
		})
	})

	Describe("Reconcile – CNP owner reference", func() {
		It("sets the workspace as owner of the CNP", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			ownerRefs := cnp.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].Kind).To(Equal("Workspace"))
			Expect(ownerRefs[0].Name).To(Equal(workspaceName))
			Expect(*ownerRefs[0].Controller).To(BeTrue())
		})
	})

	Describe("Reconcile – CNP update", func() {
		It("updates CNP when workspace spec changes from airgapped to non-airgapped", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify airgapped: 2 egress rules
			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			egress := spec.(map[string]any)["egress"].([]any)
			Expect(egress).To(HaveLen(2))

			// Update workspace to non-airgapped
			fresh := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, fresh)).To(Succeed())
			fresh.Spec.Airgapped = false
			Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

			// Reconcile again
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify non-airgapped: 3 egress rules (DNS + intra-ns + CIDR)
			cnp, err = getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			spec, _, _ = unstructured.NestedFieldCopy(cnp.Object, "spec")
			egress = spec.(map[string]any)["egress"].([]any)
			Expect(egress).To(HaveLen(3))
		})

		It("updates CNP when AllowedFQDNs list changes", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped:    false,
					AllowedFQDNs: []string{"example.com"},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Fetch CNP and check initial FQDN rule
			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			initialRV := cnp.GetResourceVersion()

			// Update workspace with different FQDNs
			fresh := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, fresh)).To(Succeed())
			fresh.Spec.AllowedFQDNs = []string{"example.com", "other.com"}
			Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

			// Reconcile again
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// CNP should have been updated (different resource version)
			cnp, err = getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetResourceVersion()).NotTo(Equal(initialRV))
		})
	})

	Describe("Reconcile – idempotency", func() {
		It("does not update CNP when reconciled twice with the same spec", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile: creates CNP
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cnp, err := getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			firstRV := cnp.GetResourceVersion()

			// Second reconcile: should be a no-op
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cnp, err = getCNP(workspaceName+"-egress", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetResourceVersion()).To(Equal(firstRV))
		})
	})

	Describe("Reconcile – Cilium CRD not installed", func() {
		It("sets NetworkPolicyReady=False with CiliumNotInstalled when CRD is missing", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					Airgapped: true,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			// Use a client wrapper that rejects CiliumNetworkPolicy operations
			// with a NoKindMatchError, simulating a cluster without Cilium.
			reconciler := &WorkspaceReconciler{
				Client:   &noCiliumClient{Client: k8sClient},
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
			}

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))

			// Verify condition is set
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonCiliumNotInstalled))
			Expect(cond.Message).To(ContainSubstring("CiliumNetworkPolicy CRD not installed"))
		})
	})
})

// findCondition returns the condition with the given type, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition { //nolint:unparam
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}

// noCiliumClient wraps a real client.Client but returns a NoKindMatchError for
// any Get or Create call targeting the CiliumNetworkPolicy GVK, simulating a
// cluster where the Cilium CRD is not installed.
type noCiliumClient struct {
	client.Client
}

var ciliumGK = schema.GroupKind{Group: "cilium.io", Kind: "CiliumNetworkPolicy"}

func isCiliumObject(obj client.Object) bool {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return false
	}
	gvk := u.GroupVersionKind()
	return gvk.Group == ciliumGK.Group && gvk.Kind == ciliumGK.Kind
}

func (c *noCiliumClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if isCiliumObject(obj) {
		return &apimeta.NoKindMatchError{GroupKind: ciliumGK, SearchedVersions: []string{"v2"}}
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *noCiliumClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if isCiliumObject(obj) {
		return &apimeta.NoKindMatchError{GroupKind: ciliumGK, SearchedVersions: []string{"v2"}}
	}
	return c.Client.Create(ctx, obj, opts...)
}
