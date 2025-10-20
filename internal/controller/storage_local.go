package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// LocalProvider implements StorageProvider for local development using hostPath volumes
type LocalProvider struct {
	BaseProvider
}

// NewLocalProvider creates a new LocalProvider for local development
func NewLocalProvider(reconciler *WorkbenchReconciler) *LocalProvider {
	return &LocalProvider{
		BaseProvider: BaseProvider{
			reconciler:      reconciler,
			storageType:     StorageTypeLocal,
			driverName:      "", // No CSI driver needed for hostPath
			secretName:      "", // No secret needed
			secretNamespace: "",
			mountType:       "local",
			pvcLabel:        "", // No label for local storage
		},
	}
}

// HasDriver always returns true for local storage (no driver needed)
func (l *LocalProvider) HasDriver(ctx context.Context, client client.Client) bool {
	// Local storage doesn't need a CSI driver, always return true if enabled
	return l.reconciler.Config.LocalStorageEnabled
}

// HasSecret always returns true for local storage (no secret needed)
func (l *LocalProvider) HasSecret(ctx context.Context, client client.Client) bool {
	// Local storage doesn't need secrets, always return true if enabled
	return l.reconciler.Config.LocalStorageEnabled
}

// Setup performs any pre-creation tasks - local storage doesn't need setup
func (l *LocalProvider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	// For hostPath volumes, Kubernetes will handle directory creation with DirectoryOrCreate
	// No setup needed like NFS directory creation or S3 bucket verification
	return nil
}

// CreatePV creates a PersistentVolume using hostPath for local storage
func (l *LocalProvider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	pvName := l.getPVName(workbench.Namespace)

	// Get the host path from config
	hostPath := l.reconciler.Config.LocalStorageHostPath
	if hostPath == "" {
		hostPath = "/tmp/workbench-local-storage"
	}

	// Use the base path directly - subPath will add workspaces/{namespace}
	// This matches S3 (path: "/") and NFS (path: share) pattern
	// Final path will be: {hostPath}/workspaces/{namespace}

	// Create PV with hostPath
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
			Labels: map[string]string{
				"workbench-namespace": workbench.Namespace,
				"storage-type":        "local",
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse("10Gi"), // Generous size for local testing
			},
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteMany,
			},
			PersistentVolumeReclaimPolicy: corev1.PersistentVolumeReclaimRetain, // Retain for local development
			StorageClassName:              "",
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: hostPath,
					Type: func() *corev1.HostPathType {
						t := corev1.HostPathDirectoryOrCreate
						return &t
					}(),
				},
			},
		},
	}

	return pv, nil
}

// CreatePVC uses the BaseProvider's CreatePVC implementation
// The BaseProvider will handle PVC creation with proper labeling and resource allocation
