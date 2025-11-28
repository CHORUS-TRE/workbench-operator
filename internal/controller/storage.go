package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// StorageType represents the type of storage backend
type StorageType string

const (
	StorageTypeS3    StorageType = "s3"
	StorageTypeNFS   StorageType = "nfs"
	StorageTypeLocal StorageType = "local"
	// Future: StorageTypeCeph, StorageTypeEFS, etc.
)

// StorageProvider interface defines the contract for storage backends
type StorageProvider interface {
	// Detection methods
	HasDriver(ctx context.Context, client client.Client) bool
	HasSecret(ctx context.Context, client client.Client) bool

	// Setup performs any pre-creation tasks (e.g., directory creation for NFS)
	Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error

	// Resource creation
	CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error)
	CreatePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolumeClaim, error)

	// Volume configuration for jobs
	// GetVolumeSpec returns a single volume (one PVC per provider)
	GetVolumeSpec(pvcName string) corev1.Volume
	// GetVolumeMountSpecs returns mount specs - may return multiple mounts from same PVC with different subpaths
	GetVolumeMountSpecs(user string, namespace string) []corev1.VolumeMount

	// Metadata
	GetPVCName(namespace string) string
	GetVolumeName() string
	GetStorageType() StorageType
	GetDriverName() string
	GetSecretName() string
	GetSecretNamespace() string
}

// =============================================================================
// StorageManager - manages all storage providers
// =============================================================================

// StorageManager manages all storage providers for a WorkbenchReconciler
type StorageManager struct {
	reconciler *WorkbenchReconciler
	providers  map[StorageType]StorageProvider
}

// NewStorageManager creates a new StorageManager with all available providers
func NewStorageManager(reconciler *WorkbenchReconciler) *StorageManager {
	providers := map[StorageType]StorageProvider{
		StorageTypeS3:  NewS3Provider(reconciler),
		StorageTypeNFS: NewNFSProvider(reconciler),
	}

	// Add local provider if enabled
	if reconciler.Config.LocalStorageEnabled {
		providers[StorageTypeLocal] = NewLocalProvider(reconciler)
	}

	return &StorageManager{
		reconciler: reconciler,
		providers:  providers,
	}
}

// GetEnabledProviders returns providers that are enabled in the workbench spec
func (sm *StorageManager) GetEnabledProviders(workbench defaultv1alpha1.Workbench) []StorageProvider {
	var enabled []StorageProvider

	storage := workbench.Spec.Storage
	if storage == nil {
		// Apply CRD defaults when storage section is missing
		storage = &defaultv1alpha1.StorageConfig{S3: true, NFS: true}
	}

	if storage.S3 {
		if provider, exists := sm.providers[StorageTypeS3]; exists {
			enabled = append(enabled, provider)
		}
	}

	if storage.NFS {
		if provider, exists := sm.providers[StorageTypeNFS]; exists {
			enabled = append(enabled, provider)
		}
	}

	// Add local provider if enabled in both config and spec
	if storage.Local {
		if provider, exists := sm.providers[StorageTypeLocal]; exists {
			enabled = append(enabled, provider)
		}
	}

	return enabled
}

// GetProvider returns the storage provider for the given type
func (sm *StorageManager) GetProvider(storageType StorageType) StorageProvider {
	return sm.providers[storageType]
}

// GetVolumeAndMountSpecs returns both volume and mount specs for all enabled storage types with PVCs
// This method assumes storage (PVs/PVCs) has already been created by ProcessEnabledStorage
func (sm *StorageManager) GetVolumeAndMountSpecs(ctx context.Context, workbench defaultv1alpha1.Workbench, user, namespace string) ([]corev1.Volume, []corev1.VolumeMount, error) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	enabledProviders := sm.GetEnabledProviders(workbench)
	for _, provider := range enabledProviders {
		// Only add volume/mount if the provider has valid secret config
		if provider.HasSecret(ctx, sm.reconciler.Client) {
			pvcName := provider.GetPVCName(workbench.Namespace)

			// Get volume spec (single volume per provider)
			volume := provider.GetVolumeSpec(pvcName)
			volumes = append(volumes, volume)

			// Get mount specs (may be multiple mounts from same PVC with different subpaths)
			mountSpecs := provider.GetVolumeMountSpecs(user, namespace)
			mounts = append(mounts, mountSpecs...)
		}
	}
	return volumes, mounts, nil
}

// ProcessEnabledStorage processes all enabled storage types and returns PVC names
// This method ensures the workbench continues to work even if some storage types fail
func (sm *StorageManager) ProcessEnabledStorage(ctx context.Context, workbench defaultv1alpha1.Workbench) (map[StorageType]string, error) {
	enabledProviders := sm.GetEnabledProviders(workbench)
	storageVolumes := make(map[StorageType]string)

	for _, provider := range enabledProviders {
		pvcName, err := sm.processStorageProvider(ctx, workbench, provider)
		// Don't fail the entire reconciliation if one storage type fails
		// Just log the error and continue - workbench should still work without this storage
		if err != nil {
			log.FromContext(ctx).Error(err, "Failed to process storage provider - continuing without it",
				"storage", provider.GetStorageType(),
				"driver", provider.GetDriverName())
			continue
		}
		if pvcName != "" {
			storageVolumes[provider.GetStorageType()] = pvcName
		}
	}

	return storageVolumes, nil
}

// processStorageProvider handles the complete lifecycle for a single storage provider
func (sm *StorageManager) processStorageProvider(ctx context.Context, workbench defaultv1alpha1.Workbench, provider StorageProvider) (string, error) {
	log := log.FromContext(ctx)

	// Check if driver is available
	if !provider.HasDriver(ctx, sm.reconciler.Client) {
		log.V(1).Info("Storage driver not detected - skipping PV/PVC creation",
			"storage", provider.GetStorageType(),
			"driver", provider.GetDriverName())

		// Driver missing - emit warning event but continue reconciliation
		errMsg := fmt.Sprintf("Storage driver '%s' not found - skipping %s storage mount. "+
			"Workbench will work without this storage. Please install the CSI driver if you need %s storage.",
			provider.GetDriverName(),
			provider.GetStorageType(),
			provider.GetStorageType())

		if sm.reconciler.Recorder != nil {
			sm.reconciler.Recorder.Event(
				&workbench,
				"Warning",
				fmt.Sprintf("%sDriverMissing", provider.GetStorageType()),
				errMsg,
			)
		}

		return "", nil // Not an error - just skip this storage type
	}

	// Check if secret exists
	if !provider.HasSecret(ctx, sm.reconciler.Client) {
		// Secret missing - emit warning event but continue reconciliation
		errMsg := fmt.Sprintf("%s driver '%s' is present but secret '%s' not found in namespace '%s'. "+
			"Skipping %s storage mount. Workbench will work without this storage. "+
			"Please create the secret if you need %s storage.",
			provider.GetStorageType(),
			provider.GetDriverName(),
			provider.GetSecretName(),
			provider.GetSecretNamespace(),
			provider.GetStorageType(),
			provider.GetStorageType())

		log.Info("Secret not found - skipping PV/PVC creation",
			"storage", provider.GetStorageType(),
			"secret", provider.GetSecretName(),
			"namespace", provider.GetSecretNamespace())

		// Emit a Kubernetes event so it's visible in kubectl describe
		if sm.reconciler.Recorder != nil {
			sm.reconciler.Recorder.Event(
				&workbench,
				"Warning",
				fmt.Sprintf("%sSecretMissing", provider.GetStorageType()),
				errMsg,
			)
		}

		return "", nil // Not an error - just skip this storage type
	}

	// Both driver and secret exist - proceed with storage setup and creation
	log.Info("Storage driver and secret detected - creating PV/PVC resources",
		"storage", provider.GetStorageType(),
		"driver", provider.GetDriverName(),
		"secret", provider.GetSecretName())

	// Create PV
	pv, err := provider.CreatePV(ctx, workbench)
	if err != nil {
		log.Error(err, "Error creating PV")
		return "", err
	}

	// Check if PV already exists
	foundPV := corev1.PersistentVolume{}
	err = sm.reconciler.Get(ctx, types.NamespacedName{Name: pv.Name}, &foundPV)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Error checking PV", "pv", pv.Name)
			return "", err
		}

		// PV doesn't exist - do setup phase (e.g., NFS directory creation) before creating PV
		if err := provider.Setup(ctx, workbench); err != nil {
			log.Error(err, "Error during storage setup")
			return "", err
		}

		log.V(1).Info("Creating PV", "pv", pv.Name)
		if err := sm.reconciler.Create(ctx, pv); err != nil {
			log.Error(err, "Error creating PV", "pv", pv.Name)
			return "", err
		}
	} else {
		log.V(1).Info("PV already exists", "pv", pv.Name)
	}

	// Create PVC
	pvc, err := provider.CreatePVC(ctx, workbench)
	if err != nil {
		log.Error(err, "Error creating PVC")
		return "", err
	}

	// Create PVC if it doesn't exist
	pvcNamespacedName := types.NamespacedName{
		Name:      pvc.Name,
		Namespace: pvc.Namespace,
	}

	foundPVC := corev1.PersistentVolumeClaim{}
	err = sm.reconciler.Get(ctx, pvcNamespacedName, &foundPVC)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Error checking PVC", "pvc", pvc.Name)
			return "", err
		}

		log.V(1).Info("Creating PVC", "pvc", pvc.Name, "namespace", pvc.Namespace)
		if err := sm.reconciler.Create(ctx, pvc); err != nil {
			log.Error(err, "Error creating PVC", "pvc", pvc.Name)
			return "", err
		}
	} else {
		log.V(1).Info("PVC already exists", "pvc", pvc.Name, "namespace", pvc.Namespace)
	}

	return provider.GetPVCName(workbench.Namespace), nil
}

// =============================================================================
// BaseProvider - Common functionality for all storage providers
// =============================================================================

// BaseProvider contains common fields and methods for all storage providers
type BaseProvider struct {
	reconciler      *WorkbenchReconciler
	storageType     StorageType
	driverName      string
	secretName      string
	secretNamespace string
	mountType       string
	pvcLabel        string
	mountAppData    bool // Whether this provider supports app_data PVC
}

// Common getters
func (b *BaseProvider) GetStorageType() StorageType { return b.storageType }
func (b *BaseProvider) GetDriverName() string       { return b.driverName }
func (b *BaseProvider) GetSecretName() string       { return b.secretName }
func (b *BaseProvider) GetSecretNamespace() string  { return b.secretNamespace }
func (b *BaseProvider) GetMountType() string        { return b.mountType }
func (b *BaseProvider) GetVolumeName() string       { return fmt.Sprintf("workspace-%s", b.mountType) }

// GetPVCName returns the PVC name for this provider
func (b *BaseProvider) GetPVCName(namespace string) string {
	return fmt.Sprintf("%s-%s-pvc", namespace, b.mountType)
}

func (b *BaseProvider) getPVName(namespace string) string {
	return fmt.Sprintf("%s-%s-pv", namespace, b.mountType)
}

// Common detection methods
func (b *BaseProvider) HasDriver(ctx context.Context, client client.Client) bool {
	return b.hasCSIDriver(ctx, client, b.driverName)
}

func (b *BaseProvider) getSecret(ctx context.Context) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := b.reconciler.Client.Get(ctx, types.NamespacedName{
		Name:      b.secretName,
		Namespace: b.secretNamespace,
	}, secret)
	if err != nil {
		return nil, fmt.Errorf("failed to get %s secret: %w", b.storageType, err)
	}

	return secret, nil
}

func (b *BaseProvider) HasSecret(ctx context.Context, client client.Client) bool {
	secret := &corev1.Secret{}
	err := client.Get(ctx, types.NamespacedName{
		Name:      b.secretName,
		Namespace: b.secretNamespace,
	}, secret)
	return err == nil
}

// GetVolumeSpec returns a single volume spec for this provider's PVC
func (b *BaseProvider) GetVolumeSpec(pvcName string) corev1.Volume {
	return corev1.Volume{
		Name: b.GetVolumeName(),
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	}
}

// GetVolumeMountSpecs returns mount specs - may return multiple mounts from same PVC with different subpaths
// For providers with mountAppData=true, this returns two mounts:
//   - data mount: /mnt/workspace-{type} with subpath workspaces/data/{namespace}
//   - app_data mount: /mnt/app_data with subpath workspaces/{namespace}/app_data/{user}
func (b *BaseProvider) GetVolumeMountSpecs(user string, namespace string) []corev1.VolumeMount {
	volumeName := b.GetVolumeName()

	// Always include the data mount
	specs := []corev1.VolumeMount{
		{
			Name:      volumeName,
			MountPath: fmt.Sprintf("/mnt/workspace-%s", b.mountType),
			SubPath:   fmt.Sprintf("workspaces/%s/data", namespace),
		},
	}

	// Add app_data mount if enabled for this provider
	if b.mountAppData {
		specs = append(specs, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: "/mnt/app_data",
			SubPath:   fmt.Sprintf("workspaces/%s/app_data/%s", namespace, user),
		})
	}

	return specs
}

// Common PV creation - needs provider-specific volumeAttributes and optional secret reference
func (b *BaseProvider) CreatePV(namespace string, volumeAttributes map[string]string, nodePublishSecretRef *corev1.SecretReference) (*corev1.PersistentVolume, error) {
	pvName := b.getPVName(namespace)

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("1Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              "",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:               b.driverName,
					VolumeHandle:         fmt.Sprintf("%s-%s", namespace, b.mountType),
					VolumeAttributes:     volumeAttributes,
					NodePublishSecretRef: nodePublishSecretRef,
				},
			},
		},
	}

	return pv, nil
}

// CreatePVC creates a single PVC bound to the PV
func (b *BaseProvider) CreatePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolumeClaim, error) {
	pvcName := b.GetPVCName(workbench.Namespace)
	pvName := b.getPVName(workbench.Namespace)

	// Only add label if pvcLabel is not empty
	labels := map[string]string{}
	if b.pvcLabel != "" {
		labels[b.pvcLabel] = "true"
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: workbench.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("1Gi"),
				},
			},
			VolumeName:       pvName,
			StorageClassName: b.getZeroStorageClass(),
		},
	}

	return pvc, nil
}

// Helper methods for BaseProvider
func (b *BaseProvider) hasCSIDriver(ctx context.Context, client client.Client, driverName string) bool {
	csiDriver := &storagev1.CSIDriver{}
	err := client.Get(ctx, types.NamespacedName{Name: driverName}, csiDriver)
	return err == nil
}

func (b *BaseProvider) getZeroStorageClass() *string {
	return new(string) // already "" by default
}
