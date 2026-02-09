package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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

	newReconciler := func() *WorkspaceReconciler {
		return &WorkspaceReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
		}
	}

	AfterEach(func() {
		workspace := &defaultv1alpha1.Workspace{}
		err := k8sClient.Get(ctx, namespacedName, workspace)
		if err == nil {
			_ = k8sClient.Delete(ctx, workspace)
		}
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

			// Re-fetch to check status
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonInvalidFQDN))
			Expect(cond.Message).To(ContainSubstring("invalid FQDN"))
		})

		It("sets NetworkPolicyReady=False with CiliumNotInstalled when CRD is missing", func() {
			// Note: envtest does not have Cilium CRDs installed, so we expect
			// the NoMatchError path to trigger.
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

			// Re-fetch to check status
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonCiliumNotInstalled))
			Expect(cond.Message).To(ContainSubstring("CiliumNetworkPolicy CRD not installed"))
		})

		It("sets NetworkPolicyReady=False for non-airgapped workspace with invalid FQDNs", func() {
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

		It("validates FQDNs pass for valid entries but Cilium is not installed", func() {
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
			// Should not return error — Cilium missing is reported via status
			Expect(err).NotTo(HaveOccurred())

			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())

			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonCiliumNotInstalled))
		})
	})
})

// findCondition returns the condition with the given type, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
