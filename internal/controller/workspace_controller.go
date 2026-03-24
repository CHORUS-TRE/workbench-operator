package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
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
	Scheme                 *runtime.Scheme
	Recorder               record.EventRecorder
	RestConfig             *rest.Config
	Registry               string
	ServicesRepository     string
	GlobalInternalServices []InternalService
}

// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workspaces,verbs=get;list;watch
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch

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

	// On first creation, set Progressing state so the user sees immediate feedback
	// before the reconcile completes.
	if workspace.Status.NetworkPolicy.Status == "" {
		workspace.Status.NetworkPolicy = defaultv1alpha1.NetworkPolicyStatus{
			Status:  defaultv1alpha1.NetworkPolicyProgressing,
			Message: "Network policy reconciliation in progress",
		}
		apimeta.SetStatusCondition(&workspace.Status.Conditions, metav1.Condition{
			Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
			Status:             metav1.ConditionFalse,
			ObservedGeneration: workspace.Generation,
			Reason:             defaultv1alpha1.ReasonProgressing,
			Message:            "Network policy reconciliation in progress",
		})
		// Best-effort: write Progressing so the user sees immediate feedback.
		// If this update fails (e.g. conflict), the reconciler is requeued and
		// the final status update will set the correct state.
		_ = r.Status().Update(ctx, &workspace)
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

		workspace.Status.NetworkPolicy = defaultv1alpha1.NetworkPolicyStatus{
			Status:  defaultv1alpha1.NetworkPolicyError,
			Message: fmt.Sprintf("Network policy not applied: %s", err.Error()),
		}
		_ = r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update workspace status after FQDN validation error", false)
		// Permanent error: user must fix the spec; requeuing won't help.
		return ctrl.Result{}, nil
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

			workspace.Status.NetworkPolicy = defaultv1alpha1.NetworkPolicyStatus{
				Status:  defaultv1alpha1.NetworkPolicyError,
				Message: "Network policy not applied: CiliumNetworkPolicy CRD not installed in the cluster",
			}
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

		workspace.Status.NetworkPolicy = defaultv1alpha1.NetworkPolicyStatus{
			Status:  defaultv1alpha1.NetworkPolicyError,
			Message: fmt.Sprintf("Network policy not applied: %s", err.Error()),
		}
		_ = r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update workspace status after reconcile error", false)

		return ctrl.Result{}, err
	}

	// Reconcile workspace services (Helm releases).
	if err := r.reconcileServices(ctx, &workspace); err != nil {
		return ctrl.Result{}, err
	}

	// Success: mirror spec mode to status. Error and Progressing states are set
	// in the error paths above; reaching here guarantees a valid applied mode.
	msg := networkPolicyStatusMessage(workspace.Spec)
	workspace.Status.NetworkPolicy = defaultv1alpha1.NetworkPolicyStatus{
		Status:  workspace.Spec.NetworkPolicy,
		Message: msg,
	}
	condition := metav1.Condition{
		Type:               defaultv1alpha1.ConditionNetworkPolicyReady,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: workspace.Generation,
		Reason:             defaultv1alpha1.ReasonApplied,
		Message:            msg,
	}

	// Status update failures should be visible in production logs; treat as transient and requeue.
	if err := r.setConditionAndUpdateStatus(ctx, &workspace, condition, "Unable to update WorkspaceStatus", false); err != nil {
		return ctrl.Result{}, err
	}

	// Poll at a short interval while any service is still transitioning.
	for _, svcStatus := range workspace.Status.Services {
		if svcStatus.Status == defaultv1alpha1.WorkspaceStatusServiceStatusProgressing {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	// Periodic resync so external Helm release changes (manual deletes, drift) are detected
	// without waiting for a spec change. Skip for service-less workspaces.
	if len(workspace.Spec.Services) > 0 {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

// reconcileNetworkPolicy creates or updates the CiliumNetworkPolicy for the
// workspace. The CNP is owned by the Workspace so garbage collection handles
// deletion automatically.
func (r *WorkspaceReconciler) reconcileNetworkPolicy(ctx context.Context, workspace *defaultv1alpha1.Workspace) error {
	validatedServices := r.validateInternalServices(ctx)
	cnp, err := buildNetworkPolicy(*workspace, validatedServices)
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

// validateInternalServices checks each configured internal service against the cluster.
// Entries whose FQDN is found as an Ingress host or LoadBalancer Service hostname are returned.
// Entries not found are skipped and an error is logged — workspace reconciliation continues.
func (r *WorkspaceReconciler) validateInternalServices(ctx context.Context) []InternalService {
	if len(r.GlobalInternalServices) == 0 {
		return nil
	}

	log := log.FromContext(ctx)

	ingressList := &networkingv1.IngressList{}
	if err := r.List(ctx, ingressList); err != nil {
		log.Error(err, "Failed to list Ingresses for internal service validation; skipping all internal services")
		return nil
	}

	svcList := &corev1.ServiceList{}
	if err := r.List(ctx, svcList); err != nil {
		log.Error(err, "Failed to list Services for internal service validation; skipping all internal services")
		return nil
	}

	var validated []InternalService
	for _, svc := range r.GlobalInternalServices {
		fqdn := normalizeFQDNEntry(svc.FQDN)
		found := false

		for _, ing := range ingressList.Items {
			for _, rule := range ing.Spec.Rules {
				if strings.EqualFold(rule.Host, fqdn) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			for _, ks := range svcList.Items {
				if ks.Spec.Type != corev1.ServiceTypeLoadBalancer {
					continue
				}
				for _, lbIng := range ks.Status.LoadBalancer.Ingress {
					if strings.EqualFold(lbIng.Hostname, fqdn) {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}

		if found {
			validated = append(validated, svc)
		} else {
			log.Error(nil, "Internal service not found as Ingress host or LoadBalancer Service hostname in cluster; skipping", "fqdn", svc.FQDN)
		}
	}

	return validated
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

func networkPolicyStatusMessage(spec defaultv1alpha1.WorkspaceSpec) string {
	switch spec.NetworkPolicy {
	case defaultv1alpha1.NetworkPolicyOpen:
		return "Network policy applied: open, all external internet traffic allowed (ports 80/443)"
	case defaultv1alpha1.NetworkPolicyFQDNAllowlist:
		return fmt.Sprintf("Network policy applied: FQDN allowlist active, allowed FQDNs: %s", strings.Join(spec.AllowedFQDNs, ", "))
	default:
		return "Network policy applied: airgapped, all external traffic blocked"
	}
}

func (r *WorkspaceReconciler) setConditionAndUpdateStatus(ctx context.Context, workspace *defaultv1alpha1.Workspace, condition metav1.Condition, logMsg string, logAtV1 bool) error {
	apimeta.SetStatusCondition(&workspace.Status.Conditions, condition)
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

// reconcileServices reconciles all workspace services defined in spec.services.
// Returns an error only for fatal infrastructure failures (e.g. Helm config init) that
// should cause an immediate requeue. Per-service errors are recorded in status instead.
func (r *WorkspaceReconciler) reconcileServices(ctx context.Context, workspace *defaultv1alpha1.Workspace) error {
	if len(workspace.Spec.Services) == 0 {
		return nil
	}

	logger := log.FromContext(ctx)
	namespace := workspace.Namespace

	if workspace.Status.Services == nil {
		workspace.Status.Services = make(map[string]defaultv1alpha1.WorkspaceStatusService)
	}

	cfg, err := newHelmConfig(namespace, r.RestConfig, logger)
	if err != nil {
		return fmt.Errorf("initializing Helm config: %w", err)
	}

	for key, svc := range workspace.Spec.Services {
		releaseName := workspace.Name + "-" + key

		registry := svc.Chart.Registry
		if registry == "" {
			registry = r.Registry
		}
		registry = strings.TrimRight(registry, "/")

		repository := svc.Chart.Repository
		if repository == "" {
			servicesRepo := strings.Trim(r.ServicesRepository, "/")
			repository = servicesRepo + "/" + key
		}
		repository = strings.Trim(repository, "/")

		repoParts := strings.Split(repository, "/")
		repoLastSegment := repoParts[len(repoParts)-1]

		chartRef := fmt.Sprintf("oci://%s/%s", registry, repository)

		secretName := ""
		if svc.Credentials != nil {
			secretName = svc.Credentials.SecretName
		}

		desiredState := svc.State
		if desiredState == "" {
			desiredState = defaultv1alpha1.WorkspaceServiceStateRunning
		}

		switch desiredState {
		case defaultv1alpha1.WorkspaceServiceStateRunning:
			userValues, err := parseServiceValues(&svc)
			if err != nil {
				logger.Error(err, "Failed to parse service values", "service", key)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: fmt.Sprintf("Failed to parse service values: %s", err.Error()),
				}
				continue
			}

			credValues, err := reconcileCredentialSecret(ctx, r.Client, namespace, workspace, svc.Credentials)
			if err != nil {
				logger.Error(err, "Failed to reconcile credential secret", "service", key)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: fmt.Sprintf("Failed to reconcile credentials: %s", err.Error()),
				}
				continue
			}

			ch, err := locateAndLoadChart(cfg, chartRef, svc.Chart.Tag, namespace)
			if err != nil {
				logger.Error(err, "Failed to locate/load chart", "service", key, "chart", chartRef)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: fmt.Sprintf("Failed to load chart: %s", err.Error()),
				}
				continue
			}
			valuesPrefix := autoValuesPrefix(ch, repoLastSegment)

			computedVals, err := evaluateComputedValues(svc.ComputedValues, releaseName, namespace, secretName)
			if err != nil {
				logger.Error(err, "Invalid computedValues", "service", key, "release", releaseName)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: err.Error(),
				}
				continue
			}
			baseVals := mergeMaps(wrapWithPrefix(userValues, valuesPrefix), wrapWithPrefix(credValues, valuesPrefix))
			finalVals := mergeMaps(baseVals, wrapWithPrefix(computedVals, valuesPrefix))
			if err := helmInstallOrUpgrade(ctx, cfg, namespace, releaseName, ch, finalVals); err != nil {
				logger.Error(err, "Failed to install/upgrade Helm release", "service", key, "release", releaseName)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: err.Error(),
				}
				continue
			}

		case defaultv1alpha1.WorkspaceServiceStateStopped:
			if _, err := helmUninstall(cfg, releaseName); err != nil {
				logger.Error(err, "Failed to uninstall Helm release", "service", key, "release", releaseName)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: err.Error(),
				}
				continue
			}

		case defaultv1alpha1.WorkspaceServiceStateDeleted:
			var deletedParts []string

			releaseDeleted, err := helmUninstall(cfg, releaseName)
			if err != nil {
				logger.Error(err, "Failed to uninstall Helm release", "service", key, "release", releaseName)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: err.Error(),
				}
				continue
			}
			if releaseDeleted {
				r.Recorder.Event(workspace, "Normal", "ServiceDeleted", fmt.Sprintf("service %s: Helm release %s uninstalled", key, releaseName))
				deletedParts = append(deletedParts, "release uninstalled")
			}

			pvcCount, err := deleteReleasePVCs(ctx, r.Client, namespace, releaseName)
			if err != nil {
				logger.Error(err, "Failed to delete PVCs", "service", key, "release", releaseName)
				workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
					Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
					Message: err.Error(),
				}
				continue
			}
			if pvcCount > 0 {
				r.Recorder.Event(workspace, "Normal", "ServiceDeleted", fmt.Sprintf("service %s: %d PVC(s) deleted", key, pvcCount))
				deletedParts = append(deletedParts, fmt.Sprintf("%d PVC(s) deleted", pvcCount))
			}

			if svc.Credentials != nil {
				credDeleted, err := deleteCredentialSecret(ctx, r.Client, namespace, svc.Credentials.SecretName)
				if err != nil {
					logger.Error(err, "Failed to delete credential secret", "service", key, "secret", svc.Credentials.SecretName)
					workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
						Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
						Message: err.Error(),
					}
					continue
				}
				if credDeleted {
					r.Recorder.Event(workspace, "Normal", "ServiceDeleted", fmt.Sprintf("service %s: credentials secret %s deleted", key, svc.Credentials.SecretName))
					deletedParts = append(deletedParts, "credentials deleted")
				}
			}

			msg := "Deleted"
			if len(deletedParts) > 0 {
				msg = "Deleted: " + strings.Join(deletedParts, ", ")
			}
			workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
				Status:  defaultv1alpha1.WorkspaceStatusServiceStatusDeleted,
				Message: msg,
			}
			continue
		}

		// Fetch status after the action so buildServiceStatus reflects the current state.
		helmStatus, helmDescription, err := helmReleaseStatus(cfg, releaseName)
		if err != nil {
			logger.Error(err, "Failed to get Helm release status", "service", key, "release", releaseName)
			workspace.Status.Services[key] = defaultv1alpha1.WorkspaceStatusService{
				Status:  defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
				Message: err.Error(),
			}
			continue
		}

		svcStatus := buildServiceStatus(helmStatus, helmDescription, svc, releaseName, secretName, workspace)

		// Override status with actual pod health when Helm thinks the release is deployed.
		// Helm marks a release as "deployed" as soon as manifests are applied, regardless of
		// whether pods are actually running — so we must check pod health separately.
		if svcStatus.Status == defaultv1alpha1.WorkspaceStatusServiceStatusRunning {
			if podStatus, podMsg := checkServicePodsHealth(ctx, r.Client, namespace, releaseName); podStatus != "" {
				svcStatus.Status = podStatus
				svcStatus.Message = podMsg
				svcStatus.ConnectionInfo = ""
			}
		}

		workspace.Status.Services[key] = svcStatus
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
