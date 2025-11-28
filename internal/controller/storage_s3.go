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
			mountAppData:    true, // S3 supports app_data PVC
		},
	}
}

// HasSecret validates that the JuiceFS secret exists and has required fields
func (s *S3Provider) HasSecret(ctx context.Context, client client.Client) bool {
	_, err := s.getSecretConfig(ctx)
	return err == nil
}

// Setup performs S3 directory creation using a Job
func (s *S3Provider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	return nil
	// // Get JuiceFS secret and filesystem name
	// fsName, err := s.getSecretConfig(ctx)
	// if err != nil {
	// 	return err
	// }

	// // Job to create S3 directories (data and app_data)
	// ttl := int32(300)   // 5 minutes cleanup
	// backoff := int32(2) // 2 retries

	// job := &batchv1.Job{
	// 	ObjectMeta: metav1.ObjectMeta{
	// 		Name:      fmt.Sprintf("s3-mkdir-%s", workbench.Namespace),
	// 		Namespace: workbench.Namespace,
	// 		Labels: map[string]string{
	// 			"app":       "s3-directory-creator",
	// 			"workbench": workbench.Name,
	// 		},
	// 	},
	// 	Spec: batchv1.JobSpec{
	// 		TTLSecondsAfterFinished: &ttl,
	// 		BackoffLimit:            &backoff,
	// 		Template: corev1.PodTemplateSpec{
	// 			Spec: corev1.PodSpec{
	// 				RestartPolicy: corev1.RestartPolicyNever,
	// 				Containers: []corev1.Container{
	// 					{
	// 						Name:  "mkdir",
	// 						Image: "busybox:latest",
	// 						Command: []string{
	// 							"sh",
	// 							"-c",
	// 							fmt.Sprintf("mkdir -p /workspaces/%s/data /workspaces/%s/app_data && chmod 2770 /workspaces/%s/data && chmod 2770 /workspaces/%s/app_data && chgrp 1001 /workspaces/%s/data /workspaces/%s/app_data",
	// 								workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace),
	// 						},
	// 						Resources: corev1.ResourceRequirements{
	// 							Limits: corev1.ResourceList{
	// 								corev1.ResourceCPU:    resource.MustParse("100m"),
	// 								corev1.ResourceMemory: resource.MustParse("64Mi"),
	// 							},
	// 							Requests: corev1.ResourceList{
	// 								corev1.ResourceCPU:    resource.MustParse("10m"),
	// 								corev1.ResourceMemory: resource.MustParse("16Mi"),
	// 							},
	// 						},
	// 						VolumeMounts: []corev1.VolumeMount{
	// 							{
	// 								Name:      "juicefs-volume",
	// 								MountPath: "/juicefs",
	// 							},
	// 						},
	// 					},
	// 				},
	// 				Volumes: []corev1.Volume{
	// 					{
	// 						Name: "juicefs-volume",
	// 						VolumeSource: corev1.VolumeSource{
	// 							CSI: &corev1.CSIVolumeSource{
	// 								Driver: s.driverName,
	// 								VolumeAttributes: map[string]string{
	// 									"name": fsName,
	// 									"path": "/",
	// 								},
	// 								NodePublishSecretRef: &corev1.LocalObjectReference{
	// 									Name: s.secretName,
	// 								},
	// 							},
	// 						},
	// 					},
	// 				},
	// 			},
	// 		},
	// 	},
	// }

	// return s.reconciler.Client.Create(ctx, job)
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
