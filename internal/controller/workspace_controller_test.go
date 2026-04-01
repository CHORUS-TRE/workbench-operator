package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

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

	testNS := NetworkPolicyNamespaces{
		AllowedIngress: []string{"test-ingress-ns"},
		AllowedEgress:  []string{"test-egress-ns"},
	}

	newReconciler := func() *WorkspaceReconciler {
		return &WorkspaceReconciler{
			Client:                  k8sClient,
			Scheme:                  k8sClient.Scheme(),
			Recorder:                record.NewFakeRecorder(10),
			NetworkPolicyNamespaces: testNS,
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
		cnp.SetName(workspaceName + "-netpol")
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
					NetworkPolicy: defaultv1alpha1.NetworkPolicyOpen,
					AllowedFQDNs:  []string{"valid.com", "not a domain!"},
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
			_, err = getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).To(HaveOccurred())
		})

		It("sets NetworkPolicyReady=False for workspace with empty FQDN entry", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyFQDNAllowlist,
					AllowedFQDNs:  []string{""},
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

		It("returns an error if existing CNP is controlled by a different object", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			// Pre-create a CNP with the expected name but a different controller owner reference.
			cnp, err := buildNetworkPolicy(*workspace, nil, testNS)
			Expect(err).NotTo(HaveOccurred())

			t := true
			cnp.SetOwnerReferences([]metav1.OwnerReference{
				{
					APIVersion:         "v1",
					Kind:               "ConfigMap",
					Name:               "not-a-workspace",
					UID:                types.UID("00000000-0000-0000-0000-000000000000"),
					Controller:         &t,
					BlockOwnerDeletion: &t,
				},
			})
			Expect(k8sClient.Create(ctx, cnp)).To(Succeed())

			reconciler := newReconciler()
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("controlled by"))
		})
	})

	Describe("Reconcile - CNP creation (happy path)", func() {
		It("creates CNP and sets NetworkPolicyReady=True for airgapped workspace", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
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
			Expect(cond.Reason).To(Equal(defaultv1alpha1.ReasonApplied))
			Expect(updated.Status.NetworkPolicy).To(Equal(defaultv1alpha1.NetworkPolicyStatus{
				Status:  defaultv1alpha1.NetworkPolicyAirgapped,
				Message: "Network policy applied: airgapped, all external traffic blocked",
			}))

			// Verify CNP was created
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetLabels()).To(HaveKeyWithValue("workspace", workspaceName))

			// Verify egress rules: Airgapped → 3 rules (kube-dns + intra-namespace endpoints + intra-namespace services)
			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(3)) // kube-dns + intra-namespace endpoints + intra-namespace services
			// Verify ingress rule restricts to same namespace
			ingress := specMap["ingress"].([]any)
			Expect(ingress).To(HaveLen(1))
		})

		It("creates CNP restricting egress to specified FQDNs", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyFQDNAllowlist,
					AllowedFQDNs:  []string{"example.com", "*.corp.internal"},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify CNP was created with 4 egress rules
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(4)) // kube-dns + intra-ns endpoints + intra-ns services + FQDN

			// Verify condition
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(updated.Status.NetworkPolicy).To(Equal(defaultv1alpha1.NetworkPolicyStatus{
				Status:  defaultv1alpha1.NetworkPolicyFQDNAllowlist,
				Message: "Network policy applied: FQDN allowlist active, allowed FQDNs: example.com, *.corp.internal",
			}))
		})

		It("creates CNP with full internet access when open and no FQDNs", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyOpen,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify CNP was created with 4 egress rules
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			specMap := spec.(map[string]any)
			egress := specMap["egress"].([]any)
			Expect(egress).To(HaveLen(4)) // kube-dns + intra-ns endpoints + intra-ns services + CIDR

			// Verify the toCIDR rule is present
			lastRule := egress[3].(map[string]any)
			Expect(lastRule).To(HaveKey("toCIDR"))

			// Verify condition
			updated := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, updated)).To(Succeed())
			cond := findCondition(updated.Status.Conditions, defaultv1alpha1.ConditionNetworkPolicyReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(updated.Status.NetworkPolicy).To(Equal(defaultv1alpha1.NetworkPolicyStatus{
				Status:  defaultv1alpha1.NetworkPolicyOpen,
				Message: "Network policy applied: open, all external internet traffic allowed (ports 80/443)",
			}))
		})

		It("updates ObservedGeneration on successful reconcile", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
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

	Describe("Reconcile - workspace deletion / CNP garbage collection", func() {
		It("CNP owner reference UID matches workspace so GC will clean it up", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			// Re-read to get server-assigned UID
			Expect(k8sClient.Get(ctx, namespacedName, workspace)).To(Succeed())
			workspaceUID := workspace.UID

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify CNP exists and its owner reference UID matches the workspace
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			ownerRefs := cnp.GetOwnerReferences()
			Expect(ownerRefs).To(HaveLen(1))
			Expect(ownerRefs[0].UID).To(Equal(workspaceUID))
			Expect(ownerRefs[0].Kind).To(Equal("Workspace"))
			Expect(ownerRefs[0].Name).To(Equal(workspaceName))
			Expect(*ownerRefs[0].Controller).To(BeTrue())
			Expect(*ownerRefs[0].BlockOwnerDeletion).To(BeTrue())
		})

		It("reconciles gracefully after workspace is deleted", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// CNP should exist
			_, err = getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())

			// Delete the workspace
			Expect(k8sClient.Delete(ctx, workspace)).To(Succeed())

			// Reconcile again — should return cleanly (workspace not found)
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(reconcile.Result{}))
		})
	})

	Describe("Reconcile - CNP update", func() {
		It("updates CNP when workspace spec changes from airgapped to open", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify Airgapped: 3 egress rules
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			spec, _, _ := unstructured.NestedFieldCopy(cnp.Object, "spec")
			egress := spec.(map[string]any)["egress"].([]any)
			Expect(egress).To(HaveLen(3))

			// Update workspace to Open (full internet)
			fresh := &defaultv1alpha1.Workspace{}
			Expect(k8sClient.Get(ctx, namespacedName, fresh)).To(Succeed())
			fresh.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyOpen
			Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

			// Reconcile again
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Verify Open: 4 egress rules (kube-dns + intra-ns endpoints + intra-ns services + CIDR)
			cnp, err = getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			spec, _, _ = unstructured.NestedFieldCopy(cnp.Object, "spec")
			egress = spec.(map[string]any)["egress"].([]any)
			Expect(egress).To(HaveLen(4))
		})

		It("updates CNP when AllowedFQDNs list changes", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyFQDNAllowlist,
					AllowedFQDNs:  []string{"example.com"},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			// Fetch CNP and check initial FQDN rule
			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
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
			cnp, err = getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetResourceVersion()).NotTo(Equal(initialRV))
		})
	})

	Describe("Reconcile - idempotency", func() {
		It("does not update CNP when reconciled twice with the same spec", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := newReconciler()

			// First reconcile: creates CNP
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cnp, err := getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			firstRV := cnp.GetResourceVersion()

			// Second reconcile: should be a no-op
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())

			cnp, err = getCNP(workspaceName+"-netpol", workspaceNamespace)
			Expect(err).NotTo(HaveOccurred())
			Expect(cnp.GetResourceVersion()).To(Equal(firstRV))
		})
	})

	Describe("Reconcile - service requeue", func() {
		It("returns 5-minute requeue when workspace has services", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
					Services: map[string]defaultv1alpha1.WorkspaceService{
						"postgres": {
							State: defaultv1alpha1.WorkspaceServiceStateStopped,
							Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			reconciler := &WorkspaceReconciler{
				Client:                  k8sClient,
				Scheme:                  k8sClient.Scheme(),
				Recorder:                record.NewFakeRecorder(10),
				RestConfig:              cfg,
				Registry:                "oci://registry.example.invalid",
				NetworkPolicyNamespaces: testNS,
			}
			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Minute))
		})
	})

	Describe("Reconcile - Cilium CRD not installed", func() {
		It("sets NetworkPolicyReady=False with CiliumNotInstalled when CRD is missing", func() {
			workspace := &defaultv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      workspaceName,
					Namespace: workspaceNamespace,
				},
				Spec: defaultv1alpha1.WorkspaceSpec{
					NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				},
			}
			Expect(k8sClient.Create(ctx, workspace)).To(Succeed())

			// Use a client wrapper that rejects CiliumNetworkPolicy operations
			// with a NoKindMatchError, simulating a cluster without Cilium.
			reconciler := &WorkspaceReconciler{
				Client:                  &noCiliumClient{Client: k8sClient},
				Scheme:                  k8sClient.Scheme(),
				Recorder:                record.NewFakeRecorder(10),
				NetworkPolicyNamespaces: testNS,
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
			Expect(updated.Status.NetworkPolicy).To(Equal(defaultv1alpha1.NetworkPolicyStatus{
				Status:  defaultv1alpha1.NetworkPolicyError,
				Message: "Network policy not applied: CiliumNetworkPolicy CRD not installed in the cluster",
			}))
		})
	})
})

var _ = Describe("dotNotationToNestedMap", func() {
	It("converts a simple key", func() {
		result, err := dotNotationToNestedMap("password", "secret")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(map[string]interface{}{"password": "secret"}))
	})

	It("converts a two-level key", func() {
		result, err := dotNotationToNestedMap("auth.password", "secret")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(map[string]interface{}{
			"auth": map[string]interface{}{"password": "secret"},
		}))
	})

	It("converts a three-level key", func() {
		result, err := dotNotationToNestedMap("settings.auth.password", "secret")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(map[string]interface{}{
			"settings": map[string]interface{}{
				"auth": map[string]interface{}{"password": "secret"},
			},
		}))
	})
})

var _ = Describe("mergeMaps", func() {
	It("merges non-overlapping keys", func() {
		result := mergeMaps(map[string]interface{}{"a": "1"}, map[string]interface{}{"b": "2"})
		Expect(result).To(HaveKeyWithValue("a", "1"))
		Expect(result).To(HaveKeyWithValue("b", "2"))
	})

	It("override takes precedence for flat keys", func() {
		result := mergeMaps(map[string]interface{}{"a": "1"}, map[string]interface{}{"a": "2"})
		Expect(result).To(HaveKeyWithValue("a", "2"))
	})

	It("deep merges nested maps preserving non-overridden keys", func() {
		base := map[string]interface{}{"auth": map[string]interface{}{"user": "admin", "password": "old"}}
		override := map[string]interface{}{"auth": map[string]interface{}{"password": "new"}}
		result := mergeMaps(base, override)
		auth := result["auth"].(map[string]interface{})
		Expect(auth).To(HaveKeyWithValue("user", "admin"))
		Expect(auth).To(HaveKeyWithValue("password", "new"))
	})

	It("handles nil override", func() {
		result := mergeMaps(map[string]interface{}{"a": "1"}, nil)
		Expect(result).To(HaveKeyWithValue("a", "1"))
	})

	It("handles nil base", func() {
		result := mergeMaps(nil, map[string]interface{}{"a": "1"})
		Expect(result).To(HaveKeyWithValue("a", "1"))
	})
})

var _ = Describe("generatePassword", func() {
	It("generates a password of the requested length", func() {
		pw, err := generatePassword(24)
		Expect(err).NotTo(HaveOccurred())
		Expect(pw).To(HaveLen(24))
	})

	It("generates unique passwords on each call", func() {
		pw1, _ := generatePassword(24)
		pw2, _ := generatePassword(24)
		Expect(pw1).NotTo(Equal(pw2))
	})
})

var _ = Describe("reconcileCredentialSecret", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "helm-test-creds"

	workspace := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "cred-test-ws", Namespace: namespace, UID: "test-uid-1234"},
	}

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("returns nil when credentials is nil", func() {
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, workspace, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeNil())
	})

	It("creates a secret with generated passwords for each value key", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"userPassword", "superuserPassword"},
		}

		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, workspace, creds)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).NotTo(BeNil())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)).To(Succeed())
		Expect(secret.Data["userPassword"]).To(HaveLen(24))
		Expect(secret.Data["superuserPassword"]).To(HaveLen(24))

		Expect(result).To(HaveKey("userPassword"))
		Expect(result).To(HaveKey("superuserPassword"))
	})

	It("converts dot-notation keys to nested Helm values", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"settings.superuserPassword"},
		}

		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, workspace, creds)
		Expect(err).NotTo(HaveOccurred())

		settings, ok := result["settings"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(settings).To(HaveKey("superuserPassword"))
	})

	It("is idempotent: returns the same passwords on repeated calls", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"userPassword"},
		}

		result1, err := reconcileCredentialSecret(ctx, k8sClient, namespace, workspace, creds)
		Expect(err).NotTo(HaveOccurred())

		result2, err := reconcileCredentialSecret(ctx, k8sClient, namespace, workspace, creds)
		Expect(err).NotTo(HaveOccurred())

		Expect(result1["userPassword"]).To(Equal(result2["userPassword"]))
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

var _ = Describe("ensureWorkspaceControllerRef", func() {
	var reconciler *WorkspaceReconciler
	BeforeEach(func() {
		reconciler = &WorkspaceReconciler{
			Client: k8sClient,
			Scheme: k8sClient.Scheme(),
		}
	})

	makeWorkspace := func(name, uid string) *defaultv1alpha1.Workspace {
		return &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
				UID:       types.UID(uid),
			},
		}
	}

	makeObj := func() *unstructured.Unstructured {
		obj := &unstructured.Unstructured{}
		obj.SetName("test-cnp")
		obj.SetNamespace("default")
		obj.SetGroupVersionKind(schema.GroupVersionKind{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy"})
		return obj
	}

	It("sets owner ref and returns true when object has no owner", func() {
		ws := makeWorkspace("ws-no-owner", "uid-1")
		obj := makeObj()
		changed, err := reconciler.ensureWorkspaceControllerRef(ws, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(metav1.GetControllerOf(obj)).NotTo(BeNil())
	})

	It("returns false and no error when the same workspace already owns the object", func() {
		ws := makeWorkspace("ws-same-owner", "uid-2")
		obj := makeObj()
		_, err := reconciler.ensureWorkspaceControllerRef(ws, obj)
		Expect(err).NotTo(HaveOccurred())

		// Call again with the same workspace — already owned, no change
		changed, err := reconciler.ensureWorkspaceControllerRef(ws, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
	})

	It("returns an error when a different workspace owns the object", func() {
		ws1 := makeWorkspace("ws-owner-a", "uid-3a")
		ws2 := makeWorkspace("ws-owner-b", "uid-3b")
		obj := makeObj()

		_, err := reconciler.ensureWorkspaceControllerRef(ws1, obj)
		Expect(err).NotTo(HaveOccurred())

		_, err = reconciler.ensureWorkspaceControllerRef(ws2, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("controlled by"))
	})

	It("updates the UID in the owner reference when the workspace was recreated", func() {
		ws := makeWorkspace("ws-uid-update", "uid-4-old")
		obj := makeObj()

		_, err := reconciler.ensureWorkspaceControllerRef(ws, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.GetControllerOf(obj).UID).To(Equal(types.UID("uid-4-old")))

		// Simulate workspace recreated: same name, new UID
		wsNew := makeWorkspace("ws-uid-update", "uid-4-new")
		changed, err := reconciler.ensureWorkspaceControllerRef(wsNew, obj)
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(metav1.GetControllerOf(obj).UID).To(Equal(types.UID("uid-4-new")))
	})
})

var _ = Describe("reconcileServices", func() {
	ctx := context.Background()
	const namespace = "default"

	newReconcilerWithHelm := func() *WorkspaceReconciler {
		return &WorkspaceReconciler{
			Client:     k8sClient,
			Scheme:     k8sClient.Scheme(),
			Recorder:   record.NewFakeRecorder(10),
			RestConfig: cfg,
			Registry:   "oci://registry.example.invalid",
		}
	}

	It("returns nil immediately when there are no services", func() {
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "no-svc-ws", Namespace: namespace},
		}
		err := newReconcilerWithHelm().reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
	})

	It("sets Failed status when credential secret is owned by another workspace", func() {
		const secretName = "shared-creds-conflict"
		// Create the secret owned by a different workspace first
		otherWS := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "other-ws-conflict", Namespace: namespace, UID: "uid-conflict-other"},
		}
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{SecretName: secretName, Paths: []string{"pw"}}
		_, err := reconcileCredentialSecret(ctx, k8sClient, namespace, otherWS, creds)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
				_ = k8sClient.Delete(ctx, secret)
			}
		})

		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "conflict-ws", Namespace: namespace, UID: "uid-conflict-this"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: "Airgapped",
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						State: defaultv1alpha1.WorkspaceServiceStateRunning,
						Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
						Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{
							SecretName: secretName,
							Paths:      []string{"pw"},
						},
					},
				},
			},
		}

		r := newReconcilerWithHelm()
		err = r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred()) // per-service errors go to status, not returned
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(ws.Status.Services["postgres"].Message).To(ContainSubstring("not owned by this Workspace"))
	})

	It("sets Failed status when service values JSON is invalid", func() {
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-values-ws", Namespace: namespace, UID: "uid-bad-values"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: "Airgapped",
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						State:  defaultv1alpha1.WorkspaceServiceStateRunning,
						Chart:  defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
						Values: &apiextensionsv1.JSON{Raw: []byte(`{not valid}`)},
					},
				},
			},
		}

		r := newReconcilerWithHelm()
		err := r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(ws.Status.Services["postgres"].Message).To(ContainSubstring("parse service values"))
	})

	It("sets Stopped status when service is Stopped and release does not exist", func() {
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "stopped-ws", Namespace: namespace, UID: "uid-stopped"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: "Airgapped",
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						State: defaultv1alpha1.WorkspaceServiceStateStopped,
						Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
					},
				},
			},
		}

		r := newReconcilerWithHelm()
		err := r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusStopped))
	})

	It("sets Deleted status when service is Deleted and release and PVCs do not exist", func() {
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "deleted-ws", Namespace: namespace, UID: "uid-deleted"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: "Airgapped",
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						State: defaultv1alpha1.WorkspaceServiceStateDeleted,
						Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
					},
				},
			},
		}

		r := newReconcilerWithHelm()
		err := r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusDeleted))
	})

	It("sets Deleted status and removes the credential secret", func() {
		const secretName = "svc-creds-to-delete"
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "creds-del-ws", Namespace: namespace, UID: "uid-creds-del"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						State: defaultv1alpha1.WorkspaceServiceStateDeleted,
						Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
						Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{
							SecretName: secretName,
							Paths:      []string{"password"},
						},
					},
				},
			},
		}

		// Pre-create the credential secret owned by this workspace
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{SecretName: secretName, Paths: []string{"password"}}
		_, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())

		DeferCleanup(func() {
			secret := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
				_ = k8sClient.Delete(ctx, secret)
			}
		})

		r := newReconcilerWithHelm()
		err = r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusDeleted))
		Expect(ws.Status.Services["postgres"].Message).To(ContainSubstring("credentials deleted"))

		// Secret should be gone
		err = k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &corev1.Secret{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred())
	})

	It("defaults empty State to Running and sets Failed when Helm install fails", func() {
		ws := &defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{Name: "default-state-ws", Namespace: namespace, UID: "uid-default-state"},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: "Airgapped",
				Services: map[string]defaultv1alpha1.WorkspaceService{
					"postgres": {
						// State is empty — should default to Running
						Chart: defaultv1alpha1.WorkspaceServiceChart{Tag: "1.0.0"},
					},
				},
			},
		}

		r := newReconcilerWithHelm()
		err := r.reconcileServices(ctx, ws)
		Expect(err).NotTo(HaveOccurred())
		// Helm install fails (no real registry) — status should be Failed
		Expect(ws.Status.Services["postgres"].Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
	})
})

var _ = Describe("ValidateInternalServices", func() {
	ctx := context.Background()

	newHTTPRoute := func(name, namespace string, hostnames ...gatewayv1.Hostname) *gatewayv1.HTTPRoute {
		return &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec:       gatewayv1.HTTPRouteSpec{Hostnames: hostnames},
		}
	}

	newFakeClient := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().
			WithScheme(k8sClient.Scheme()).
			WithObjects(objs...).
			Build()
	}

	It("returns nil for an empty list", func() {
		err := ValidateInternalServices(ctx, newFakeClient(), nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("succeeds when FQDN matches an HTTPRoute hostname", func() {
		route := newHTTPRoute("gitlab-httproute", "envoy-gateway-system", "gitlab.chorus-tre.ch")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("is case-insensitive when matching FQDN", func() {
		route := newHTTPRoute("gitlab-httproute", "envoy-gateway-system", "GITLAB.CHORUS-TRE.CH")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("succeeds when the HTTPRoute is in any namespace (cluster-wide search)", func() {
		route := newHTTPRoute("gitlab-httproute", "other-ns", "gitlab.chorus-tre.ch")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns an error when FQDN is not found anywhere", func() {
		err := ValidateInternalServices(ctx, newFakeClient(), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("gitlab.chorus-tre.ch"))
	})

	It("does not match an HTTPRoute with no hostnames (catch-all route should not satisfy a specific FQDN)", func() {
		// Gateway API allows omitting Hostnames (matches all hostnames), but such a route
		// should not count as satisfying a specific FQDN entry — require explicit hostnames.
		route := newHTTPRoute("catch-all", "envoy-gateway-system")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("gitlab.chorus-tre.ch"))
	})

	It("returns an error when HTTPRoute listing fails", func() {
		c := fake.NewClientBuilder().
			WithScheme(k8sClient.Scheme()).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*gatewayv1.HTTPRouteList); ok {
						return fmt.Errorf("httproute list error")
					}
					return c.List(ctx, list, opts...)
				},
			}).
			Build()
		err := ValidateInternalServices(ctx, c, []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("httproute list error"))
	})

	It("returns an error when two entries declare the same FQDN (case-insensitive)", func() {
		route := newHTTPRoute("gitlab-httproute", "envoy-gateway-system", "gitlab.chorus-tre.ch")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
			{FQDN: "GITLAB.CHORUS-TRE.CH", Ports: []string{"443"}},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate internal service FQDN"))
	})

	It("returns an error on the first failing entry when multiple services are configured", func() {
		route := newHTTPRoute("gitlab-httproute", "envoy-gateway-system", "gitlab.chorus-tre.ch")
		err := ValidateInternalServices(ctx, newFakeClient(route), []InternalService{
			{FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
			{FQDN: "i2b2.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("i2b2.chorus-tre.ch"))
	})
})
