package controller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
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
	StorageTypeS3  StorageType = "s3"
	StorageTypeNFS StorageType = "nfs"
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

	// Resource cleanup
	DeletePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) error

	// Volume configuration for jobs
	GetVolumeSpec(pvcName string) corev1.Volume
	GetVolumeMountSpec(user string, namespace string) corev1.VolumeMount

	// Metadata
	GetPVCName(namespace string) string
	GetMountPath(user string) string
	GetStorageType() StorageType
	GetDriverName() string
	GetSecretName() string
	GetSecretNamespace() string
	GetVolumeName() string
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
	return &StorageManager{
		reconciler: reconciler,
		providers: map[StorageType]StorageProvider{
			StorageTypeS3:  NewS3Provider(reconciler),
			StorageTypeNFS: NewNFSProvider(reconciler),
		},
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

			volume := provider.GetVolumeSpec(pvcName)
			volumes = append(volumes, volume)

			mount := provider.GetVolumeMountSpec(user, namespace)
			mounts = append(mounts, mount)
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

// DeleteStorageResources deletes all storage resources (PVCs) for enabled storage providers
// PVs are automatically deleted due to PersistentVolumeReclaimDelete policy
func (sm *StorageManager) DeleteStorageResources(ctx context.Context, workbench defaultv1alpha1.Workbench) (int, error) {
	log := log.FromContext(ctx)
	deletedCount := 0

	enabledProviders := sm.GetEnabledProviders(workbench)
	if len(enabledProviders) == 0 {
		log.V(1).Info("No storage providers enabled - skipping storage cleanup")
		return 0, nil
	}

	var allErrors []error

	for _, provider := range enabledProviders {
		err := provider.DeletePVC(ctx, workbench)
		if err != nil {
			log.V(1).Error(err, "Failed to delete PVC", "storage", provider.GetStorageType())
			allErrors = append(allErrors, err)
		} else {
			deletedCount++
			log.V(1).Info("Successfully deleted PVC",
				"storage", provider.GetStorageType(),
				"pvc", provider.GetPVCName(workbench.Namespace))
		}
	}

	// Return the first error if any occurred
	if len(allErrors) > 0 {
		return deletedCount, allErrors[0]
	}

	return deletedCount, nil
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
}

// Common getters
func (b *BaseProvider) GetStorageType() StorageType { return b.storageType }
func (b *BaseProvider) GetDriverName() string       { return b.driverName }
func (b *BaseProvider) GetSecretName() string       { return b.secretName }
func (b *BaseProvider) GetSecretNamespace() string  { return b.secretNamespace }
func (b *BaseProvider) GetMountType() string        { return b.mountType }
func (b *BaseProvider) GetVolumeName() string       { return fmt.Sprintf("workspace-%s", b.mountType) }

// Common computed methods
func (b *BaseProvider) GetPVCName(namespace string) string {
	return fmt.Sprintf("%s-%s-pvc", namespace, b.mountType)
}

func (b *BaseProvider) getPVName(namespace string) string {
	return fmt.Sprintf("%s-%s-pv", namespace, b.mountType)
}

func (b *BaseProvider) GetMountPath(user string) string {
	return fmt.Sprintf("/home/%s/workspace-%s", user, b.mountType)
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

// Common volume methods
func (b *BaseProvider) GetVolumeSpec(pvcName string) corev1.Volume {
	return b.createVolumeSpec(pvcName)
}

func (b *BaseProvider) GetVolumeMountSpec(user string, namespace string) corev1.VolumeMount {
	return b.createVolumeMountSpec(b.GetMountPath(user), namespace)
}

// Common resource management - DeletePVC implemented directly
func (b *BaseProvider) DeletePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	pvcName := b.GetPVCName(workbench.Namespace)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: workbench.Namespace,
		},
	}

	err := b.reconciler.Client.Delete(ctx, pvc)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete %s PVC %s: %w", b.storageType, pvcName, err)
	}

	return nil
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
func (b *BaseProvider) createVolumeSpec(pvcName string) corev1.Volume {
	return corev1.Volume{
		Name: b.GetVolumeName(),
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	}
}

func (b *BaseProvider) createVolumeMountSpec(mountPath, namespace string) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      b.GetVolumeName(),
		MountPath: mountPath,
		SubPath:   b.getWorkspaceSubPath(namespace),
	}
}

func (b *BaseProvider) hasCSIDriver(ctx context.Context, client client.Client, driverName string) bool {
	csiDriver := &storagev1.CSIDriver{}
	err := client.Get(ctx, types.NamespacedName{Name: driverName}, csiDriver)
	return err == nil
}

func (b *BaseProvider) getZeroStorageClass() *string {
	return new(string) // already "" by default
}

func (b *BaseProvider) getWorkspaceSubPath(namespace string) string {
	return fmt.Sprintf("workspaces/%s", namespace)
}

// =============================================================================
// Provider Constructors
// =============================================================================

func NewS3Provider(reconciler *WorkbenchReconciler) *S3Provider {
	return &S3Provider{
		BaseProvider: BaseProvider{
			reconciler:      reconciler,
			storageType:     StorageTypeS3,
			driverName:      "csi.juicefs.com",
			secretName:      reconciler.Config.JuiceFSSecretName,
			secretNamespace: reconciler.Config.JuiceFSSecretNamespace,
			mountType:       "archive",
			pvcLabel:        "use-juicefs",
		},
	}
}

func NewNFSProvider(reconciler *WorkbenchReconciler) *NFSProvider {
	return &NFSProvider{
		BaseProvider: BaseProvider{
			reconciler:      reconciler,
			storageType:     StorageTypeNFS,
			driverName:      "nfs.csi.k8s.io",
			secretName:      reconciler.Config.NFSSecretName,
			secretNamespace: reconciler.Config.NFSSecretNamespace,
			mountType:       "scratch",
			pvcLabel:        "", // No label for NFS
		},
	}
}

// =============================================================================
// S3Provider - JuiceFS/S3 storage implementation
// =============================================================================

// S3Provider implements StorageProvider for JuiceFS/S3 storage
type S3Provider struct {
	BaseProvider
}

// HasSecret validates that the JuiceFS secret exists and has required fields
func (s *S3Provider) HasSecret(ctx context.Context, client client.Client) bool {
	_, err := s.getSecretConfig(ctx)
	return err == nil
}

// Setup performs any pre-creation tasks - S3 doesn't need any setup
func (s *S3Provider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	return nil
}

// getSecretConfig gets the JuiceFS secret and extracts bucket
func (s *S3Provider) getSecretConfig(ctx context.Context) (string, error) {
	secret, err := s.BaseProvider.getSecret(ctx)
	if err != nil {
		return "", err
	}

	bucket := string(secret.Data["bucket"])
	if bucket == "" {
		return "", fmt.Errorf("JuiceFS secret missing required 'bucket' field")
	}

	return bucket, nil
}

// CreatePV creates a PersistentVolume for S3 storage
func (s *S3Provider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	bucket, err := s.getSecretConfig(ctx)
	if err != nil {
		return nil, err
	}

	volumeAttributes := map[string]string{
		"bucket":  bucket,
		"subPath": fmt.Sprintf("workspaces/%s", workbench.Namespace),
	}

	// JuiceFS requires NodePublishSecretRef for authentication
	nodePublishSecretRef := &corev1.SecretReference{
		Name:      s.secretName,
		Namespace: s.secretNamespace,
	}

	return s.BaseProvider.CreatePV(workbench.Namespace, volumeAttributes, nodePublishSecretRef)
}

// =============================================================================
// NFSProvider - NFS storage implementation
// =============================================================================

// NFSProvider implements StorageProvider for NFS storage
type NFSProvider struct {
	BaseProvider
}

// HasSecret validates that the NFS secret exists and has required fields
func (n *NFSProvider) HasSecret(ctx context.Context, client client.Client) bool {
	_, _, err := n.getSecretConfig(ctx)
	return err == nil
}

// Setup performs NFS directory creation using a Job
func (n *NFSProvider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	// Get NFS secret and extract server/share
	server, share, err := n.getSecretConfig(ctx)
	if err != nil {
		return err
	}

	// Create Job to create NFS directory
	ttl := int32(300)   // 5 minutes cleanup
	backoff := int32(2) // 2 retries

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("nfs-mkdir-%s", workbench.Namespace),
			Namespace: workbench.Namespace,
			Labels: map[string]string{
				"app":       "nfs-directory-creator",
				"workbench": workbench.Name,
			},
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:  "mkdir",
							Image: "busybox:latest",
							Command: []string{
								"mkdir",
								"-p",
								fmt.Sprintf("/nfs/workspaces/%s", workbench.Namespace),
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("16Mi"),
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "nfs-volume",
									MountPath: "/nfs",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "nfs-volume",
							VolumeSource: corev1.VolumeSource{
								CSI: &corev1.CSIVolumeSource{
									Driver: n.GetDriverName(),
									VolumeAttributes: map[string]string{
										"server": server,
										"share":  share,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	return n.reconciler.Client.Create(ctx, job)
}

// getSecretConfig gets the NFS secret and extracts server and share
func (n *NFSProvider) getSecretConfig(ctx context.Context) (string, string, error) {
	secret, err := n.BaseProvider.getSecret(ctx)
	if err != nil {
		return "", "", err
	}

	server := string(secret.Data["server"])
	share := string(secret.Data["share"])

	if server == "" {
		return "", "", fmt.Errorf("NFS secret missing required 'server' field")
	}
	if share == "" {
		return "", "", fmt.Errorf("NFS secret missing required 'share' field")
	}

	return server, share, nil
}

// CreatePV creates a PersistentVolume for NFS storage
func (n *NFSProvider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	server, share, err := n.getSecretConfig(ctx)
	if err != nil {
		return nil, err
	}

	volumeAttributes := map[string]string{
		"server": server,
		"share":  share,
	}

	// NFS doesn't require NodePublishSecretRef
	return n.BaseProvider.CreatePV(workbench.Namespace, volumeAttributes, nil)
}
