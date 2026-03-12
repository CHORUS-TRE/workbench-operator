package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	clientcmd "k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/storage/driver"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// restClientGetter implements genericclioptions.RESTClientGetter for Helm.
type restClientGetter struct {
	namespace  string
	restConfig *rest.Config
}

func (r *restClientGetter) ToRESTConfig() (*rest.Config, error) {
	return r.restConfig, nil
}

func (r *restClientGetter) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	disco, err := discovery.NewDiscoveryClientForConfig(r.restConfig)
	if err != nil {
		return nil, err
	}
	return memory.NewMemCacheClient(disco), nil
}

func (r *restClientGetter) ToRESTMapper() (apimeta.RESTMapper, error) {
	disco, err := r.ToDiscoveryClient()
	if err != nil {
		return nil, err
	}
	return restmapper.NewDeferredDiscoveryRESTMapper(disco), nil
}

func (r *restClientGetter) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{
		Context: clientcmdapi.Context{Namespace: r.namespace},
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
}

// newHelmConfig returns an action.Configuration scoped to the given namespace.
// Helm internal logs are forwarded to logger at V(1) to aid troubleshooting (e.g. OCI pull failures).
func newHelmConfig(namespace string, restConfig *rest.Config, logger logr.Logger) (*action.Configuration, error) {
	getter := &restClientGetter{namespace: namespace, restConfig: restConfig}

	registryClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating Helm registry client: %w", err)
	}

	cfg := new(action.Configuration)
	cfg.RegistryClient = registryClient

	helmLog := func(format string, v ...interface{}) {
		logger.V(1).Info(fmt.Sprintf(format, v...))
	}
	if err := cfg.Init(getter, namespace, "secret", helmLog); err != nil {
		return nil, fmt.Errorf("initializing Helm action config: %w", err)
	}

	return cfg, nil
}

// helmInstallOrUpgrade installs the chart if the release doesn't exist, or upgrades it otherwise.
// chartRef is the full OCI reference, e.g. oci://harbor.build.chorus-tre.local/services/postgres.
// chartVersion is the semver chart version, e.g. 1.6.1.
//
// Note: Upgrade.Install is purely informational in the Helm SDK (v3.16+) and does NOT handle the
// missing-release case automatically. We split install vs upgrade explicitly, mirroring `helm upgrade --install`,
// with cross-fallbacks to handle TOCTOU races between concurrent reconciles.
func helmInstallOrUpgrade(ctx context.Context, cfg *action.Configuration, namespace, releaseName, chartRef, chartVersion string, values map[string]interface{}) error {
	settings := cli.New()
	settings.SetNamespace(namespace)

	// Use NewInstall to locate the chart — its constructor propagates cfg.RegistryClient
	// into ChartPathOptions.registryClient (unexported), which LocateChart needs for OCI.
	locator := action.NewInstall(cfg)
	locator.Version = chartVersion
	chartPath, err := locator.ChartPathOptions.LocateChart(chartRef, settings)
	if err != nil {
		return fmt.Errorf("locating chart %s: %w", chartRef, err)
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("loading chart %s: %w", chartPath, err)
	}

	newInstall := func() error {
		a := action.NewInstall(cfg)
		a.Namespace = namespace
		a.ReleaseName = releaseName
		a.Wait = false
		a.Atomic = false
		_, err := a.RunWithContext(ctx, ch, values)
		return err
	}

	newUpgrade := func() error {
		a := action.NewUpgrade(cfg)
		a.Namespace = namespace
		a.Wait = false
		a.Atomic = false
		a.MaxHistory = 10
		a.ChartPathOptions.Version = chartVersion
		_, err := a.RunWithContext(ctx, releaseName, ch, values)
		return err
	}

	var releaseStatus string
	statusAction := action.NewStatus(cfg)
	if rel, err := statusAction.Run(releaseName); err != nil {
		if strings.Contains(err.Error(), driver.ErrReleaseNotFound.Error()) {
			releaseStatus = "not-found"
		} else {
			return fmt.Errorf("checking release %s: %w", releaseName, err)
		}
	} else {
		releaseStatus = string(rel.Info.Status)
		// Skip upgrade when already deployed with the same chart version and values.
		if releaseStatus == "deployed" && rel.Chart.Metadata.Version == chartVersion && releaseValuesMatch(rel.Config, values) {
			return nil
		}
	}

	if releaseStatus == "not-found" {
		// Release absent: install. If a concurrent reconcile already installed it, fall back to upgrade.
		if err := newInstall(); err != nil {
			if strings.Contains(err.Error(), driver.ErrReleaseExists.Error()) {
				if err := newUpgrade(); err != nil {
					return fmt.Errorf("helm upgrade %s: %w", releaseName, err)
				}
				return nil
			}
			return fmt.Errorf("helm install %s: %w", releaseName, err)
		}
	} else {
		// Release present: upgrade. If a concurrent reconcile removed it, fall back to install.
		if err := newUpgrade(); err != nil {
			if strings.Contains(err.Error(), driver.ErrNoDeployedReleases.Error()) {
				if err := newInstall(); err != nil {
					return fmt.Errorf("helm install %s: %w", releaseName, err)
				}
				return nil
			}
			return fmt.Errorf("helm upgrade %s: %w", releaseName, err)
		}
	}

	return nil
}

// releaseValuesMatch compares two Helm values maps by JSON serialization.
// Returns true when both maps are semantically equal.
func releaseValuesMatch(deployed, desired map[string]interface{}) bool {
	a, err1 := json.Marshal(deployed)
	b, err2 := json.Marshal(desired)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(a) == string(b)
}

// helmUninstall uninstalls a Helm release and reports whether it was present.
// If the release does not exist, it is a no-op and returns (false, nil).
func helmUninstall(cfg *action.Configuration, releaseName string) (bool, error) {
	uninstallAction := action.NewUninstall(cfg)
	uninstallAction.KeepHistory = false
	uninstallAction.Wait = false

	if _, err := uninstallAction.Run(releaseName); err != nil {
		if strings.Contains(err.Error(), driver.ErrReleaseNotFound.Error()) {
			return false, nil
		}
		return false, fmt.Errorf("helm uninstall %s: %w", releaseName, err)
	}

	return true, nil
}

// helmReleaseStatus returns the Helm release status string and description, or "not-found" if the release doesn't exist.
func helmReleaseStatus(cfg *action.Configuration, releaseName string) (string, string, error) {
	statusAction := action.NewStatus(cfg)
	rel, err := statusAction.Run(releaseName)
	if err != nil {
		if strings.Contains(err.Error(), driver.ErrReleaseNotFound.Error()) {
			return "not-found", "", nil
		}
		return "", "", err
	}
	return string(rel.Info.Status), rel.Info.Description, nil
}

// deleteReleasePVCs deletes all PVCs labeled with the Helm release instance name.
// Returns the count of PVCs deleted.
func deleteReleasePVCs(ctx context.Context, k8sClient client.Client, namespace, releaseName string) (int, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := k8sClient.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": releaseName},
	); err != nil {
		return 0, fmt.Errorf("listing PVCs for release %s: %w", releaseName, err)
	}

	deleted := 0
	for i := range pvcList.Items {
		if err := k8sClient.Delete(ctx, &pvcList.Items[i]); err != nil {
			if !apierrors.IsNotFound(err) {
				return deleted, fmt.Errorf("deleting PVC %s: %w", pvcList.Items[i].Name, err)
			}
			// already gone between List and Delete — don't count
		} else {
			deleted++
		}
	}

	return deleted, nil
}

// deleteCredentialSecret deletes the credential Secret for a service and reports whether it was present.
// If the Secret does not exist, it is a no-op and returns (false, nil).
func deleteCredentialSecret(ctx context.Context, k8sClient client.Client, namespace, secretName string) (bool, error) {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("getting credential secret %s: %w", secretName, err)
	}
	if err := k8sClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("deleting credential secret %s: %w", secretName, err)
	}
	return true, nil
}

// generatePassword returns a random URL-safe base64 password of the given length.
// Reads exactly ceil(length*3/4) random bytes — the minimum needed to produce `length`
// base64url characters — giving length×6 bits of entropy (e.g. 24 chars → 144 bits).
func generatePassword(length int) (string, error) {
	b := make([]byte, (length*3+3)/4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:length], nil
}

// reconcileCredentialSecret creates or reads the credential Secret and returns
// the passwords as a nested Helm values map. Idempotent: existing passwords are never rotated.
func reconcileCredentialSecret(ctx context.Context, k8sClient client.Client, namespace string, workspace *defaultv1alpha1.Workspace, creds *defaultv1alpha1.WorkspaceServiceCredentials) (map[string]interface{}, error) {
	if creds == nil {
		return nil, nil
	}

	secret := &corev1.Secret{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: creds.SecretName, Namespace: namespace}, secret)

	if apierrors.IsNotFound(err) {
		data := make(map[string][]byte)
		for _, key := range creds.Paths {
			pw, err := generatePassword(24)
			if err != nil {
				return nil, fmt.Errorf("generating password for %s: %w", key, err)
			}
			data[key] = []byte(pw)
		}

		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      creds.SecretName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(workspace, defaultv1alpha1.GroupVersion.WithKind("Workspace")),
				},
			},
			Data: data,
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return nil, fmt.Errorf("creating credential secret %s: %w", creds.SecretName, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("getting credential secret %s: %w", creds.SecretName, err)
	} else {
		// Secret already exists — verify it belongs to this Workspace to prevent
		// cross-workspace credential leakage when two Workspaces share a secretName.
		owned := false
		for _, ref := range secret.OwnerReferences {
			if ref.UID == workspace.UID {
				owned = true
				break
			}
		}
		if !owned {
			return nil, fmt.Errorf("credential secret %s already exists and is not owned by this Workspace", creds.SecretName)
		}
	}

	helmValues := make(map[string]interface{})
	for _, key := range creds.Paths {
		val, ok := secret.Data[key]
		if !ok {
			continue
		}
		helmValues = mergeMaps(helmValues, dotNotationToNestedMap(key, string(val)))
	}

	return helmValues, nil
}

// dotNotationToNestedMap converts "a.b.c" → {"a": {"b": {"c": value}}}.
func dotNotationToNestedMap(key, value string) map[string]interface{} {
	parts := strings.Split(key, ".")
	result := make(map[string]interface{})
	current := result
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = value
		} else {
			next := make(map[string]interface{})
			current[part] = next
			current = next
		}
	}
	return result
}

// wrapWithPrefix wraps values under a single top-level key, mirroring the Helm
// sub-chart convention where a wrapper chart passes values to its dependency
// under the dependency name (e.g. postgres.settings.superuserPassword).
// When prefix is empty or values is empty, the original map is returned unchanged.
func wrapWithPrefix(values map[string]interface{}, prefix string) map[string]interface{} {
	if prefix == "" || len(values) == 0 {
		return values
	}
	return map[string]interface{}{prefix: values}
}

// mergeMaps merges override into base recursively. Override values take precedence.
func mergeMaps(base, override map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range base {
		result[k] = v
	}
	for k, v := range override {
		if baseVal, ok := result[k]; ok {
			if baseMap, ok := baseVal.(map[string]interface{}); ok {
				if overrideMap, ok := v.(map[string]interface{}); ok {
					result[k] = mergeMaps(baseMap, overrideMap)
					continue
				}
			}
		}
		result[k] = v
	}
	return result
}

// checkServicePodsHealth inspects pods belonging to a Helm release and returns a status override
// when pods are unhealthy. Returns ("", "") when pods are healthy or no pods exist yet.
func checkServicePodsHealth(ctx context.Context, k8sClient client.Client, namespace, releaseName string) (defaultv1alpha1.WorkspaceStatusServiceStatus, string) {
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": releaseName},
	); err != nil || len(podList.Items) == 0 {
		return "", ""
	}

	allReady := true
	for _, pod := range podList.Items {
		// Pod-level failure
		if pod.Status.Phase == corev1.PodFailed {
			return defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
				fmt.Sprintf("pod %s failed: %s", pod.Name, pod.Status.Message)
		}

		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				if reason == "CrashLoopBackOff" || reason == "OOMKilled" || reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					msg := fmt.Sprintf("container %s: %s", cs.Name, reason)
					if cs.State.Waiting.Message != "" {
						msg += ": " + cs.State.Waiting.Message
					}
					return defaultv1alpha1.WorkspaceStatusServiceStatusFailed, msg
				}
				allReady = false
			}
			if !cs.Ready {
				allReady = false
			}
		}
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				if reason == "CrashLoopBackOff" || reason == "OOMKilled" || reason == "ImagePullBackOff" || reason == "ErrImagePull" {
					return defaultv1alpha1.WorkspaceStatusServiceStatusFailed,
						fmt.Sprintf("init container %s: %s", cs.Name, reason)
				}
				allReady = false
			}
		}
	}

	if !allReady {
		return defaultv1alpha1.WorkspaceStatusServiceStatusProgressing, "Pods are starting"
	}
	return "", ""
}

// buildServiceStatus maps Helm release state + desired spec state to a WorkspaceStatusService.
func buildServiceStatus(helmStatus, helmDescription string, svc defaultv1alpha1.WorkspaceService, releaseName, secretName string, workspace *defaultv1alpha1.Workspace) defaultv1alpha1.WorkspaceStatusService {
	var state defaultv1alpha1.WorkspaceStatusServiceStatus

	switch {
	case helmStatus == "deployed" && svc.State == defaultv1alpha1.WorkspaceServiceStateRunning:
		state = defaultv1alpha1.WorkspaceStatusServiceStatusRunning
	case helmStatus == "pending-install" || helmStatus == "pending-upgrade" || helmStatus == "pending-rollback":
		state = defaultv1alpha1.WorkspaceStatusServiceStatusProgressing
	case helmStatus == "failed":
		state = defaultv1alpha1.WorkspaceStatusServiceStatusFailed
	case helmStatus == "not-found" && svc.State == defaultv1alpha1.WorkspaceServiceStateStopped:
		state = defaultv1alpha1.WorkspaceStatusServiceStatusStopped
	case helmStatus == "not-found" && svc.State == defaultv1alpha1.WorkspaceServiceStateDeleted:
		state = defaultv1alpha1.WorkspaceStatusServiceStatusDeleted
	default:
		state = defaultv1alpha1.WorkspaceStatusServiceStatusProgressing
	}

	connectionInfo := ""
	if svc.ConnectionInfoTemplate != "" && state == defaultv1alpha1.WorkspaceStatusServiceStatusRunning {
		connectionInfo = strings.NewReplacer(
			"{{.Namespace}}", workspace.Namespace,
			"{{.ReleaseName}}", releaseName,
			"{{.SecretName}}", secretName,
		).Replace(svc.ConnectionInfoTemplate)
	}

	return defaultv1alpha1.WorkspaceStatusService{
		Status:         state,
		Message:        helmDescription,
		ConnectionInfo: connectionInfo,
		SecretName:     secretName,
	}
}

// parseServiceValues unmarshals the raw JSON values from a WorkspaceService into a map.
func parseServiceValues(svc *defaultv1alpha1.WorkspaceService) (map[string]interface{}, error) {
	userValues := make(map[string]interface{})
	if svc.Values != nil {
		if err := json.Unmarshal(svc.Values.Raw, &userValues); err != nil {
			return nil, fmt.Errorf("parsing service values: %w", err)
		}
	}
	return userValues, nil
}
