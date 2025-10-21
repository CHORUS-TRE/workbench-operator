package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// =============================================================================
// S3Provider - JuiceFS/S3 storage implementation
// =============================================================================

// S3Provider implements StorageProvider for JuiceFS/S3 storage
type S3Provider struct {
	BaseProvider
}

// NewS3Provider creates a new S3Provider for JuiceFS/S3 storage
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

// HasSecret validates that the JuiceFS secret exists and has required fields
func (s *S3Provider) HasSecret(ctx context.Context, client client.Client) bool {
	_, err := s.getSecretConfig(ctx)
	return err == nil
}

// Setup performs any pre-creation tasks - S3 doesn't need any setup
func (s *S3Provider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	return nil
}

// getSecretConfig gets the JuiceFS secret and extracts filesystem name
func (s *S3Provider) getSecretConfig(ctx context.Context) (string, error) {
	secret, err := s.BaseProvider.getSecret(ctx)
	if err != nil {
		return "", err
	}

	name := string(secret.Data["name"])
	if name == "" {
		return "", fmt.Errorf("JuiceFS secret missing required 'name' field")
	}

	return name, nil
}

// CreatePV creates a PersistentVolume for S3 storage
func (s *S3Provider) CreatePV(ctx context.Context, workbench defaultv1alpha1.Workbench) (*corev1.PersistentVolume, error) {
	fsName, err := s.getSecretConfig(ctx)
	if err != nil {
		return nil, err
	}

	volumeAttributes := map[string]string{
		"name": fsName,
		"path": "/",
	}

	// JuiceFS requires NodePublishSecretRef for authentication
	nodePublishSecretRef := &corev1.SecretReference{
		Name:      s.secretName,
		Namespace: s.secretNamespace,
	}

	return s.BaseProvider.CreatePV(workbench.Namespace, volumeAttributes, nodePublishSecretRef)
}
