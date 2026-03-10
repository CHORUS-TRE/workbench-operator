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
func helmInstallOrUpgrade(ctx context.Context, cfg *action.Configuration, namespace, releaseName, chartRef, chartVersion string, values map[string]interface{}) error {
	settings := cli.New()
	settings.SetNamespace(namespace)

	upgradeAction := action.NewUpgrade(cfg)
	upgradeAction.Install = true
	upgradeAction.Namespace = namespace
	upgradeAction.Wait = false
	upgradeAction.Atomic = false
	upgradeAction.ChartPathOptions.Version = chartVersion

	chartPath, err := upgradeAction.ChartPathOptions.LocateChart(chartRef, settings)
	if err != nil {
		return fmt.Errorf("locating chart %s: %w", chartRef, err)
	}

	ch, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("loading chart %s: %w", chartPath, err)
	}

	if _, err := upgradeAction.RunWithContext(ctx, releaseName, ch, values); err != nil {
		return fmt.Errorf("helm install/upgrade %s: %w", releaseName, err)
	}

	return nil
}

// helmUninstall uninstalls a Helm release. If the release does not exist, it is a no-op.
func helmUninstall(cfg *action.Configuration, releaseName string) error {
	uninstallAction := action.NewUninstall(cfg)
	uninstallAction.KeepHistory = false
	uninstallAction.Wait = false

	if _, err := uninstallAction.Run(releaseName); err != nil {
		if strings.Contains(err.Error(), driver.ErrReleaseNotFound.Error()) {
			return nil
		}
		return fmt.Errorf("helm uninstall %s: %w", releaseName, err)
	}

	return nil
}

// helmReleaseStatus returns the Helm release status string, or "not-found" if the release doesn't exist.
func helmReleaseStatus(cfg *action.Configuration, releaseName string) (string, error) {
	statusAction := action.NewStatus(cfg)
	rel, err := statusAction.Run(releaseName)
	if err != nil {
		if strings.Contains(err.Error(), driver.ErrReleaseNotFound.Error()) {
			return "not-found", nil
		}
		return "", err
	}
	return string(rel.Info.Status), nil
}

// deleteReleasePVCs deletes all PVCs labeled with the Helm release instance name.
func deleteReleasePVCs(ctx context.Context, k8sClient client.Client, namespace, releaseName string) error {
	pvcList := &corev1.PersistentVolumeClaimList{}
	if err := k8sClient.List(ctx, pvcList,
		client.InNamespace(namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": releaseName},
	); err != nil {
		return fmt.Errorf("listing PVCs for release %s: %w", releaseName, err)
	}

	for i := range pvcList.Items {
		if err := k8sClient.Delete(ctx, &pvcList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting PVC %s: %w", pvcList.Items[i].Name, err)
		}
	}

	return nil
}

// deleteCredentialSecret deletes the credential Secret for a service. If the Secret does not exist, it is a no-op.
func deleteCredentialSecret(ctx context.Context, k8sClient client.Client, namespace, secretName string) error {
	secret := &corev1.Secret{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting credential secret %s: %w", secretName, err)
	}
	if err := k8sClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting credential secret %s: %w", secretName, err)
	}
	return nil
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

// buildServiceStatus maps Helm release state + desired spec state to a WorkspaceStatusService.
func buildServiceStatus(helmStatus string, svc defaultv1alpha1.WorkspaceService, releaseName, secretName string, workspace *defaultv1alpha1.Workspace) defaultv1alpha1.WorkspaceStatusService {
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
		ConnectionInfo: connectionInfo,
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
