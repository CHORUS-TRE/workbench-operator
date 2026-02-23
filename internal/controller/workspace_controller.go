package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// WorkspaceReconciler reconciles a Workspace object
type WorkspaceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile ensures the CiliumNetworkPolicy for this Workspace matches the
// desired state derived from the WorkspaceSpec.
func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(1).Info("Reconcile Workspace", "what", req.NamespacedName)

	// Fetch the workspace to reconcile.
	workspace := defaultv1alpha1.Workspace{}
	if err := r.Get(ctx, req.NamespacedName, &workspace); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "unable to fetch the workspace")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Validate FQDNs before attempting reconciliation.
	if err := validateFQDNs(workspace.Spec.AllowedFQDNs); err != nil {
		log.V(1).Info("Invalid FQDN in workspace spec", "error", err)
		condition := metav1.Condition{
			Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workspace.Generation,
			Reason:             defaultv1alpha1.ReasonInvalidFQDN,
			Message:            fmt.Sprintf("Network policy not applied: %s", err.Error()),
		}

		r.Recorder.Event(
			&workspace,
			"Warning",
			"InvalidFQDN",
			fmt.Sprintf("Invalid AllowedFQDNs entry: %s", err.Error()),
		)

		_ = r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update workspace status after FQDN validation error", false)
		// Permanent error: user must fix the spec; requeuing won't help.
		return ctrl.Result{}, nil
	}

	if workspace.Spec.Airgapped && len(workspace.Spec.AllowedFQDNs) > 0 {
		r.Recorder.Event(
			&workspace,
			"Warning",
			"FQDNsIgnored",
			"AllowedFQDNs provided but Airgapped=true; FQDNs will be ignored",
		)
	}

	// Reconcile the CiliumNetworkPolicy.
	if err := r.reconcileNetworkPolicy(ctx, &workspace); err != nil {
		if apimeta.IsNoMatchError(err) {
			log.V(1).Info("CiliumNetworkPolicy CRD not found in cluster")
			condition := metav1.Condition{
				Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: workspace.Generation,
				Reason:             defaultv1alpha1.ReasonCiliumNotInstalled,
				Message:            "Network policy not applied: CiliumNetworkPolicy CRD not installed in the cluster",
			}

			r.Recorder.Event(
				&workspace,
				"Warning",
				"CiliumNotInstalled",
				"CiliumNetworkPolicy CRD not found — network policies cannot be enforced",
			)

			_ = r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update workspace status after Cilium CRD not found", false)
			// Permanent error: Cilium must be installed; requeuing won't help.
			return ctrl.Result{}, nil
		}

		log.Error(err, "Error reconciling network policy", "workspace", workspace.Name)
		condition := metav1.Condition{
			Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workspace.Generation,
			Reason:             defaultv1alpha1.ReasonReconcileError,
			Message:            fmt.Sprintf("Network policy not applied: %s", err.Error()),
		}

		_ = r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update workspace status after reconcile error", false)

		return ctrl.Result{}, err
	}

	// Success.
	condition := metav1.Condition{
		Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: workspace.Generation,
		Reason:             defaultv1alpha1.ReasonReconciled,
		Message:            "Network policy applied successfully",
	}

	// Status update failures should be visible in production logs; treat as transient and requeue.
	if err := r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update WorkspaceStatus", false); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileNetworkPolicy creates or updates the CiliumNetworkPolicy for the
// workspace. The CNP is owned by the Workspace so garbage collection handles
// deletion automatically.
func (r *WorkspaceReconciler) reconcileNetworkPolicy(ctx context.Context, workspace *defaultv1alpha1.Workspace) error {
	cnp, err := buildNetworkPolicy(*workspace)
	if err != nil {
		return err
	}

	if err := controllerutil.SetControllerReference(workspace, cnp, r.Scheme); err != nil {
		return err
	}

	key := types.NamespacedName{
		Name:      cnp.GetName(),
		Namespace: cnp.GetNamespace(),
	}

	existing := unstructured.Unstructured{}
	existing.SetGroupVersionKind(cnp.GroupVersionKind())

	if err := r.Get(ctx, key, &existing); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, cnp)
		}
		return err
	}

	updated := false

	// Normalize both specs through a JSON round-trip so that type
	// differences introduced by the API server (e.g. port 53 as float64
	// vs string "53") don't cause false-positive diffs every reconcile.
	normalizeViaJSON := func(v any) (any, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out any
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	normalizedDesired, err := normalizeViaJSON(cnp.Object["spec"])
	if err != nil {
		return err
	}
	normalizedExisting, err := normalizeViaJSON(existing.Object["spec"])
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalizedDesired, normalizedExisting) {
		existing.Object["spec"] = normalizedDesired
		updated = true
	}
	if !reflect.DeepEqual(existing.GetLabels(), cnp.GetLabels()) {
		existing.SetLabels(cnp.GetLabels())
		updated = true
	}

	ownerUpdated, err := r.ensureWorkspaceControllerRef(workspace, &existing)
	if err != nil {
		return err
	}
	if ownerUpdated {
		updated = true
	}

	if updated {
		return r.Update(ctx, &existing)
	}

	return nil
}

func (r *WorkspaceReconciler) ensureWorkspaceControllerRef(workspace *defaultv1alpha1.Workspace, obj metav1.Object) (bool, error) {
	// Ensure the CNP is controlled by this Workspace. If another controller owns it,
	// treat it as an error rather than silently skipping.
	owner := metav1.GetControllerOf(obj)
	if owner == nil {
		if err := controllerutil.SetControllerReference(workspace, obj, r.Scheme); err != nil {
			return false, err
		}
		return true, nil
	}

	expectedAPIVersion := defaultv1alpha1.GroupVersion.String()
	if owner.APIVersion != expectedAPIVersion || owner.Kind != "Workspace" || owner.Name != workspace.Name {
		return false, fmt.Errorf("existing CiliumNetworkPolicy %s/%s is controlled by %s %s/%s, expected Workspace %s/%s",
			obj.GetNamespace(), obj.GetName(),
			owner.Kind, owner.APIVersion, owner.Name,
			expectedAPIVersion, workspace.Name,
		)
	}

	// Same workspace name/kind: if UID changed (workspace recreated), update the owner ref.
	if owner.UID != workspace.UID && workspace.UID != "" {
		refs := obj.GetOwnerReferences()
		for i := range refs {
			if refs[i].Controller != nil && *refs[i].Controller &&
				refs[i].APIVersion == expectedAPIVersion &&
				refs[i].Kind == "Workspace" &&
				refs[i].Name == workspace.Name {
				refs[i].UID = workspace.UID
				obj.SetOwnerReferences(refs)
				return true, nil
			}
		}
		return false, fmt.Errorf("controller owner reference exists but could not be updated for %s/%s", obj.GetNamespace(), obj.GetName())
	}

	return false, nil
}

// setCondition sets or updates a condition on the workspace status.
func (r *WorkspaceReconciler) setCondition(workspace *defaultv1alpha1.Workspace, condition metav1.Condition) {
	apimeta.SetStatusCondition(&workspace.Status.Conditions, condition)
}

func (r *WorkspaceReconciler) setConditionAndUpdateStatus(ctx context.Context, workspace *defaultv1alpha1.Workspace, condition metav1.Condition, logMsg string, logAtV1 bool) error {
	r.setCondition(workspace, condition)
	workspace.Status.ObservedGeneration = workspace.Generation

	if err := r.Status().Update(ctx, workspace); err != nil {
		if logMsg != "" {
			logger := log.FromContext(ctx)
			if logAtV1 {
				logger.V(1).Error(err, logMsg)
			} else {
				logger.Error(err, logMsg)
			}
		}
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumNetworkPolicy",
	})

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&defaultv1alpha1.Workspace{})

	// Only add Owns watch if the CiliumNetworkPolicy CRD is installed.
	// This allows the operator to start without Cilium; the reconciler
	// handles the missing-CRD case gracefully at reconciliation time.
	// After Cilium is installed, a controller restart picks up the watch.
	if _, err := mgr.GetRESTMapper().RESTMapping(
		schema.GroupKind{Group: "cilium.io", Kind: "CiliumNetworkPolicy"},
		"v2",
	); err == nil {
		builder = builder.Owns(cnp)
	} else {
		log.Log.Info("CiliumNetworkPolicy CRD not found, skipping Owns watch (network policies will still be reconciled)")
	}

	return builder.Complete(r)
}
