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

// Setup performs local storage directory creation using a Job
func (l *LocalProvider) Setup(ctx context.Context, workbench defaultv1alpha1.Workbench) error {
	// Get the host path from config
	hostPath := l.reconciler.Config.LocalStorageHostPath
	if hostPath == "" {
		hostPath = "/tmp/workbench-local-storage"
	}

	// Create Job to create local storage directories (data and config)
	ttl := int32(300)   // 5 minutes cleanup
	backoff := int32(2) // 2 retries

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("local-mkdir-%s", workbench.Namespace),
			Namespace: workbench.Namespace,
			Labels: map[string]string{
				"app":       "local-directory-creator",
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
								"sh",
								"-c",
								fmt.Sprintf("mkdir -p /local/workspaces/%s/data /local/workspaces/%s/app_data && chmod 2770 /local/workspaces/%s/data && chmod 2770 /local/workspaces/%s/app_data && chgrp 1001 /local/workspaces/%s/data /local/workspaces/%s/app_data",
									workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace, workbench.Namespace),
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
									Name:      "local-volume",
									MountPath: "/local",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "local-volume",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: hostPath,
									Type: func() *corev1.HostPathType {
										t := corev1.HostPathDirectoryOrCreate
										return &t
									}(),
								},
							},
						},
					},
				},
			},
		},
	}

	return l.reconciler.Client.Create(ctx, job)
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
				corev1.ResourceStorage: resource.MustParse("10Gi"),
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
