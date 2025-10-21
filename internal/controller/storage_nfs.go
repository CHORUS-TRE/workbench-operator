package controller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// =============================================================================
// NFSProvider - NFS storage implementation
// =============================================================================

// NFSProvider implements StorageProvider for NFS storage
type NFSProvider struct {
	BaseProvider
}

// NewNFSProvider creates a new NFSProvider for NFS storage
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
								NFS: &corev1.NFSVolumeSource{
									Server: server,
									Path:   share,
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
