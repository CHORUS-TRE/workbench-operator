package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

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
			StorageTypeS3:  &S3Provider{reconciler: reconciler},
			StorageTypeNFS: &NFSProvider{reconciler: reconciler},
		},
	}
}

// GetEnabledProviders returns providers that are enabled in the workbench spec
func (sm *StorageManager) GetEnabledProviders(workbench defaultv1alpha1.Workbench) []StorageProvider {
	var enabled []StorageProvider

	if workbench.Spec.Storage == nil {
		return enabled
	}

	if workbench.Spec.Storage.S3 {
		if provider, exists := sm.providers[StorageTypeS3]; exists {
			enabled = append(enabled, provider)
		}
	}

	if workbench.Spec.Storage.NFS {
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
func (sm *StorageManager) GetVolumeAndMountSpecs(ctx context.Context, workbench defaultv1alpha1.Workbench, user, namespace string) ([]corev1.Volume, []corev1.VolumeMount, error) {
	storageVolumes, err := sm.ProcessEnabledStorage(ctx, workbench)
	if err != nil {
		return nil, nil, err
	}

	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	for storageType, pvcName := range storageVolumes {
		if pvcName != "" {
			provider := sm.GetProvider(storageType)
			if provider != nil {
				volume := provider.GetVolumeSpec(pvcName)
				volumes = append(volumes, volume)

				mount := provider.GetVolumeMountSpec(user, namespace)
				mounts = append(mounts, mount)
			}
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
		log.V(1).Info(fmt.Sprintf("Storage driver '%s' not detected - skipping %s PV/PVC creation",
			provider.GetDriverName(), provider.GetStorageType()),
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

		log.V(1).Info(fmt.Sprintf("Secret '%s' not found in namespace '%s' - skipping %s PV/PVC creation",
			provider.GetSecretName(), provider.GetSecretNamespace(), provider.GetStorageType()),
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
	log.V(1).Info(fmt.Sprintf("Storage driver '%s' and secret '%s' detected - creating %s PV/PVC resources",
		provider.GetDriverName(), provider.GetSecretName(), provider.GetStorageType()),
		"driver", provider.GetDriverName(),
		"secret", provider.GetSecretName())

	// Create PV
	pv, err := provider.CreatePV(ctx, workbench)
	if err != nil {
		log.V(1).Error(err, "Error creating PV")
		return "", err
	}

	// Check if PV already exists
	foundPV := corev1.PersistentVolume{}
	err = sm.reconciler.Get(ctx, types.NamespacedName{Name: pv.Name}, &foundPV)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.V(1).Error(err, "Error checking PV", "pv", pv.Name)
			return "", err
		}

		// PV doesn't exist - do setup phase (e.g., NFS directory creation) before creating PV
		if err := provider.Setup(ctx, workbench); err != nil {
			log.V(1).Error(err, "Error during storage setup")
			return "", err
		}

		log.V(1).Info("Creating PV", "pv", pv.Name)
		if err := sm.reconciler.Create(ctx, pv); err != nil {
			log.V(1).Error(err, "Error creating PV", "pv", pv.Name)
			return "", err
		}
	} else {
		log.V(1).Info("PV already exists", "pv", pv.Name)
	}

	// Create PVC
	pvc, err := provider.CreatePVC(ctx, workbench)
	if err != nil {
		log.V(1).Error(err, "Error creating PVC")
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
			log.V(1).Error(err, "Error checking PVC", "pvc", pvc.Name)
			return "", err
		}

		log.V(1).Info("Creating PVC", "pvc", pvc.Name, "namespace", pvc.Namespace)
		if err := sm.reconciler.Create(ctx, pvc); err != nil {
			log.V(1).Error(err, "Error creating PVC", "pvc", pvc.Name)
			return "", err
		}
	} else {
		log.V(1).Info("PVC already exists", "pvc", pvc.Name, "namespace", pvc.Namespace)
	}

	return provider.GetPVCName(workbench.Namespace), nil
}

// =============================================================================
// S3Provider - JuiceFS/S3 storage implementation
// =============================================================================

// S3Provider implements StorageProvider for JuiceFS/S3 storage
type S3Provider struct {
	reconciler *WorkbenchReconciler
}

// HasDriver checks if the JuiceFS CSI driver is installed in the cluster
func (s *S3Provider) HasDriver(ctx context.Context, client client.Client) bool {
	return hasCSIDriver(ctx, client, s.GetDriverName())
}

// HasSecret checks if the configured JuiceFS secret exists
func (s *S3Provider) HasSecret(ctx context.Context, client client.Client) bool {
	secret := &corev1.Secret{}
	err := client.Get(ctx, types.NamespacedName{
		Name:      s.reconciler.Config.JuiceFSSecretName,
		Namespace: s.reconciler.Config.JuiceFSSecretNamespace,
	}, secret)
	return err == nil
}

// Setup performs any pre-creation tasks - S3 doesn't need any setup
func (s *S3Provider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	// S3/JuiceFS doesn't require any setup like directory creation
	return nil
}

// CreatePV creates a PersistentVolume for S3 storage
func (s *S3Provider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	pvName := s.getPVName(workbench.Namespace)

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
			StorageClassName:              "", // Empty string for direct binding
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "csi.juicefs.com",
					VolumeHandle: fmt.Sprintf("juicefs-%s", workbench.Namespace),
					NodePublishSecretRef: &corev1.SecretReference{
						Name:      s.reconciler.Config.JuiceFSSecretName,
						Namespace: s.reconciler.Config.JuiceFSSecretNamespace,
					},
				},
			},
		},
	}

	return pv, nil
}

// CreatePVC creates a PersistentVolumeClaim for S3 storage
func (s *S3Provider) CreatePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolumeClaim, error) {
	pvcName := s.GetPVCName(workbench.Namespace)
	pvName := s.getPVName(workbench.Namespace)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: workbench.Namespace,
			Labels: map[string]string{
				"use-juicefs": "true",
			},
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
			StorageClassName: getZeroStorageClass(),
		},
	}

	return pvc, nil
}

// DeletePVC deletes the PVC for S3 storage
func (s *S3Provider) DeletePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	return deletePVC(ctx, s.reconciler.Client, s.GetPVCName(workbench.Namespace), workbench.Namespace, string(s.GetStorageType()))
}

// GetVolumeSpec returns the volume specification for job pods
func (s *S3Provider) GetVolumeSpec(pvcName string) corev1.Volume {
	return createVolumeSpec(s.GetVolumeName(), pvcName)
}

// GetVolumeMountSpec returns the volume mount specification for job containers
func (s *S3Provider) GetVolumeMountSpec(user string, namespace string) corev1.VolumeMount {
	return createVolumeMountSpec(s.GetVolumeName(), s.GetMountPath(user), namespace)
}

// GetPVCName returns the PVC name for this storage type
func (s *S3Provider) GetPVCName(namespace string) string {
	return fmt.Sprintf("%s-s3-pvc", namespace)
}

// getPVName returns the PV name for this storage type
func (s *S3Provider) getPVName(namespace string) string {
	return fmt.Sprintf("%s-s3-pv", namespace)
}

// GetMountPath returns the mount path for this storage type
func (s *S3Provider) GetMountPath(user string) string {
	return fmt.Sprintf("/home/%s/workspace-archive", user)
}

// GetStorageType returns the storage type identifier
func (s *S3Provider) GetStorageType() StorageType {
	return StorageTypeS3
}

// GetDriverName returns the CSI driver name
func (s *S3Provider) GetDriverName() string {
	return "csi.juicefs.com"
}

// GetSecretName returns the secret name
func (s *S3Provider) GetSecretName() string {
	return s.reconciler.Config.JuiceFSSecretName
}

// GetSecretNamespace returns the secret namespace
func (s *S3Provider) GetSecretNamespace() string {
	return s.reconciler.Config.JuiceFSSecretNamespace
}

// GetVolumeName returns the volume name for this storage type
func (s *S3Provider) GetVolumeName() string {
	return "workspace-archive"
}

// =============================================================================
// NFSProvider - NFS storage implementation
// =============================================================================

// NFSProvider implements StorageProvider for NFS storage
type NFSProvider struct {
	reconciler *WorkbenchReconciler
}

// HasDriver checks if the NFS CSI driver is installed in the cluster
func (n *NFSProvider) HasDriver(ctx context.Context, client client.Client) bool {
	return hasCSIDriver(ctx, client, n.GetDriverName())
}

// HasSecret checks if the NFS secret exists
func (n *NFSProvider) HasSecret(ctx context.Context, client client.Client) bool {
	secret := &corev1.Secret{}
	err := client.Get(ctx, types.NamespacedName{
		Name:      n.GetSecretName(),
		Namespace: n.GetSecretNamespace(),
	}, secret)
	return err == nil
}

// Setup performs NFS directory creation
func (n *NFSProvider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	// Get the NFS secret for directory creation
	nfsSecret := &corev1.Secret{}
	err := n.reconciler.Client.Get(ctx, types.NamespacedName{
		Name:      n.GetSecretName(),
		Namespace: n.GetSecretNamespace(),
	}, nfsSecret)
	if err != nil {
		return fmt.Errorf("failed to get NFS secret: %w", err)
	}

	// Create the workspace directory in NFS
	return n.ensureNFSWorkspaceDirectory(ctx, workbench, nfsSecret)
}

// CreatePV creates a PersistentVolume for NFS storage
func (n *NFSProvider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	pvName := n.getPVName(workbench.Namespace)

	// Get NFS secret for server details
	nfsSecret := &corev1.Secret{}
	err := n.reconciler.Client.Get(ctx, types.NamespacedName{
		Name:      n.GetSecretName(),
		Namespace: n.GetSecretNamespace(),
	}, nfsSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get NFS secret: %w", err)
	}

	server, share, err := n.getNFSSecretConfig(nfsSecret)
	if err != nil {
		return nil, err
	}

	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"),
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimDelete,
			StorageClassName:              "", // Empty string for direct binding
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "nfs.csi.k8s.io",
					VolumeHandle: fmt.Sprintf("nfs-%s", workbench.Namespace),
					VolumeAttributes: map[string]string{
						"server": server,
						"share":  share,
					},
				},
			},
		},
	}

	return pv, nil
}

// CreatePVC creates a PersistentVolumeClaim for NFS storage
func (n *NFSProvider) CreatePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolumeClaim, error) {
	pvcName := n.GetPVCName(workbench.Namespace)
	pvName := n.getPVName(workbench.Namespace)

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: workbench.Namespace,
			Labels: map[string]string{
				"use-nfs": "true",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("10Gi"),
				},
			},
			VolumeName:       pvName,
			StorageClassName: getZeroStorageClass(),
		},
	}

	return pvc, nil
}

// DeletePVC deletes the PVC for NFS storage
func (n *NFSProvider) DeletePVC(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	return deletePVC(ctx, n.reconciler.Client, n.GetPVCName(workbench.Namespace), workbench.Namespace, string(n.GetStorageType()))
}

// GetVolumeSpec returns the volume specification for job pods
func (n *NFSProvider) GetVolumeSpec(pvcName string) corev1.Volume {
	return createVolumeSpec(n.GetVolumeName(), pvcName)
}

// GetVolumeMountSpec returns the volume mount specification for job containers
func (n *NFSProvider) GetVolumeMountSpec(user string, namespace string) corev1.VolumeMount {
	return createVolumeMountSpec(n.GetVolumeName(), n.GetMountPath(user), namespace)
}

// GetPVCName returns the PVC name for this storage type
func (n *NFSProvider) GetPVCName(namespace string) string {
	return fmt.Sprintf("%s-nfs-pvc", namespace)
}

// getPVName returns the PV name for this storage type
func (n *NFSProvider) getPVName(namespace string) string {
	return fmt.Sprintf("%s-nfs-pv", namespace)
}

// GetMountPath returns the mount path for this storage type
func (n *NFSProvider) GetMountPath(user string) string {
	return fmt.Sprintf("/home/%s/workspace-scratch", user)
}

// GetStorageType returns the storage type identifier
func (n *NFSProvider) GetStorageType() StorageType {
	return StorageTypeNFS
}

// GetDriverName returns the CSI driver name
func (n *NFSProvider) GetDriverName() string {
	return "nfs.csi.k8s.io"
}

// GetSecretName returns the secret name
func (n *NFSProvider) GetSecretName() string {
	return n.reconciler.Config.NFSSecretName
}

// GetSecretNamespace returns the secret namespace
func (n *NFSProvider) GetSecretNamespace() string {
	return n.reconciler.Config.NFSSecretNamespace
}

// GetVolumeName returns the volume name for this storage type
func (n *NFSProvider) GetVolumeName() string {
	return "workspace-scratch"
}

// getNFSSecretConfig validates and extracts server and share from NFS secret
func (n *NFSProvider) getNFSSecretConfig(nfsSecret *corev1.Secret) (string, string, error) {
	server := string(nfsSecret.Data["server"])
	share := string(nfsSecret.Data["share"])

	if server == "" {
		return "", "", fmt.Errorf("NFS secret missing required 'server' field")
	}
	if share == "" {
		return "", "", fmt.Errorf("NFS secret missing required 'share' field")
	}

	return server, share, nil
}

// ensureNFSWorkspaceDirectory creates the workspace directory in NFS if it doesn't exist
func (n *NFSProvider) ensureNFSWorkspaceDirectory(ctx context.Context, workbench defaultv1alpha1.Workbench, nfsSecret *corev1.Secret) error {
	log := log.FromContext(ctx)

	// Validate namespace for path safety (prevent directory traversal)
	if strings.Contains(workbench.Namespace, "..") || strings.Contains(workbench.Namespace, "/") {
		return fmt.Errorf("invalid namespace for NFS directory creation: %s", workbench.Namespace)
	}

	// Validate and extract NFS secret data
	server, share, err := n.getNFSSecretConfig(nfsSecret)
	if err != nil {
		return err
	}

	// Create temporary pod to create directory
	dirCreatorPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("nfs-dir-creator-%s", workbench.Namespace),
			Namespace: workbench.Namespace,
			Labels: map[string]string{
				"app":       "nfs-directory-creator",
				"workbench": workbench.Name,
			},
		},
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
							Driver: "nfs.csi.k8s.io",
							VolumeAttributes: map[string]string{
								"server": server,
								"share":  share,
							},
						},
					},
				},
			},
		},
	}

	// Create the pod
	if err := n.reconciler.Client.Create(ctx, dirCreatorPod); err != nil {
		return fmt.Errorf("failed to create directory creation pod: %w", err)
	}

	// Wait for pod completion (with timeout)
	timeout := time.After(60 * time.Second)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			// Cleanup the pod on timeout
			_ = n.reconciler.Client.Delete(ctx, dirCreatorPod)
			return fmt.Errorf("directory creation pod timed out")
		case <-ticker.C:
			var pod corev1.Pod
			err := n.reconciler.Client.Get(ctx, types.NamespacedName{
				Name:      dirCreatorPod.Name,
				Namespace: dirCreatorPod.Namespace,
			}, &pod)
			if err != nil {
				continue
			}

			if pod.Status.Phase == corev1.PodSucceeded {
				log.V(1).Info("NFS workspace directory created successfully")
				// Cleanup the pod
				_ = n.reconciler.Client.Delete(ctx, dirCreatorPod)
				return nil
			} else if pod.Status.Phase == corev1.PodFailed {
				// Cleanup the pod
				_ = n.reconciler.Client.Delete(ctx, dirCreatorPod)
				return fmt.Errorf("directory creation pod failed")
			}
		}
	}
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
			log.V(1).Error(err, fmt.Sprintf("Failed to delete %s PVC", provider.GetStorageType()))
			allErrors = append(allErrors, err)
		} else {
			deletedCount++
			log.V(1).Info(fmt.Sprintf("Successfully deleted %s PVC", provider.GetStorageType()),
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
// Helper functions
// =============================================================================

// deletePVC is a shared helper function to delete PVCs for any storage type
func deletePVC(ctx context.Context, client client.Client, pvcName, namespace, storageType string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
	}

	err := client.Delete(ctx, pvc)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete %s PVC %s: %w", storageType, pvcName, err)
	}

	return nil
}

// createVolumeSpec is a shared helper function to create volume specs for PVCs
func createVolumeSpec(volumeName, pvcName string) corev1.Volume {
	return corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	}
}

// createVolumeMountSpec is a shared helper function to create volume mount specs
func createVolumeMountSpec(volumeName, mountPath, namespace string) corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      volumeName,
		MountPath: mountPath,
		SubPath:   getWorkspaceSubPath(namespace),
	}
}

// hasCSIDriver is a shared helper function to check if a CSI driver is installed
func hasCSIDriver(ctx context.Context, client client.Client, driverName string) bool {
	csiDriver := &storagev1.CSIDriver{}
	err := client.Get(ctx, types.NamespacedName{Name: driverName}, csiDriver)
	return err == nil
}

// Helper function to get zero storage class pointer
func getZeroStorageClass() *string {
	return new(string) // already "" by default
}

// getWorkspaceSubPath returns the common subpath for workspace storage
func getWorkspaceSubPath(namespace string) string {
	return fmt.Sprintf("workspaces/%s", namespace)
}
