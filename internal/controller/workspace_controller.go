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
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;patch;delete
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
		r.setCondition(&workspace, metav1.Condition{
			Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workspace.Generation,
			Reason:             defaultv1alpha1.ReasonInvalidFQDN,
			Message:            fmt.Sprintf("Network policy not applied: %s", err.Error()),
		})

		r.Recorder.Event(
			&workspace,
			"Warning",
			"InvalidFQDN",
			fmt.Sprintf("Invalid AllowedFQDNs entry: %s", err.Error()),
		)

		return ctrl.Result{}, r.Status().Update(ctx, &workspace)
	}

	// Reconcile the CiliumNetworkPolicy.
	if err := r.reconcileNetworkPolicy(ctx, &workspace); err != nil {
		if apimeta.IsNoMatchError(err) {
			log.V(1).Info("CiliumNetworkPolicy CRD not found in cluster")
			r.setCondition(&workspace, metav1.Condition{
				Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
				Status:             metav1.ConditionFalse,
				ObservedGeneration: workspace.Generation,
				Reason:             defaultv1alpha1.ReasonCiliumNotInstalled,
				Message:            "Network policy not applied: CiliumNetworkPolicy CRD not installed in the cluster",
			})

			r.Recorder.Event(
				&workspace,
				"Warning",
				"CiliumNotInstalled",
				"CiliumNetworkPolicy CRD not found — network policies cannot be enforced",
			)

			return ctrl.Result{}, r.Status().Update(ctx, &workspace)
		}

		log.Error(err, "Error reconciling network policy", "workspace", workspace.Name)
		r.setCondition(&workspace, metav1.Condition{
			Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workspace.Generation,
			Reason:             defaultv1alpha1.ReasonReconcileError,
			Message:            fmt.Sprintf("Network policy not applied: %s", err.Error()),
		})

		if statusErr := r.Status().Update(ctx, &workspace); statusErr != nil {
			log.Error(statusErr, "Unable to update workspace status after reconcile error")
		}

		return ctrl.Result{}, err
	}

	// Success.
	r.setCondition(&workspace, metav1.Condition{
		Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: workspace.Generation,
		Reason:             defaultv1alpha1.ReasonReconciled,
		Message:            "Network policy applied successfully",
	})

	// Update observed generation.
	workspace.Status.ObservedGeneration = workspace.Generation

	if err := r.Status().Update(ctx, &workspace); err != nil {
		log.V(1).Error(err, "Unable to update WorkspaceStatus")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// reconcileNetworkPolicy creates or updates the CiliumNetworkPolicy for the
// workspace. The CNP is owned by the Workspace so garbage collection handles
// deletion automatically.
func (r *WorkspaceReconciler) reconcileNetworkPolicy(ctx context.Context, workspace *defaultv1alpha1.Workspace) error {
	cnp := buildNetworkPolicy(*workspace)

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

	desiredJSON, err := json.Marshal(cnp.Object["spec"])
	if err != nil {
		return err
	}
	existingJSON, err := json.Marshal(existing.Object["spec"])
	if err != nil {
		return err
	}
	if string(desiredJSON) != string(existingJSON) {
		var normalizedSpec any
		if err := json.Unmarshal(desiredJSON, &normalizedSpec); err != nil {
			return err
		}
		existing.Object["spec"] = normalizedSpec
		updated = true
	}
	if !reflect.DeepEqual(existing.GetLabels(), cnp.GetLabels()) {
		existing.SetLabels(cnp.GetLabels())
		updated = true
	}

	if !controllerutil.HasControllerReference(&existing) {
		if err := controllerutil.SetControllerReference(workspace, &existing, r.Scheme); err != nil {
			return err
		}
		updated = true
	}

	if updated {
		return r.Update(ctx, &existing)
	}

	return nil
}

// setCondition sets or updates a condition on the workspace status.
func (r *WorkspaceReconciler) setCondition(workspace *defaultv1alpha1.Workspace, condition metav1.Condition) {
	apimeta.SetStatusCondition(&workspace.Status.Conditions, condition)
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	cnp := &unstructured.Unstructured{}
	cnp.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cilium.io",
		Version: "v2",
		Kind:    "CiliumNetworkPolicy",
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&defaultv1alpha1.Workspace{}).
		Owns(cnp).
		Complete(r)
}
