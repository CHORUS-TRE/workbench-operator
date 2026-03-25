package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/yaml"
)

// LicenseConfig represents the parsed license configuration from the license Secret's config.yaml key.
type LicenseConfig struct {
	Licenses map[string]LicenseEntry `json:"licenses"`
}

// LicenseEntry defines how a single app's license is injected.
type LicenseEntry struct {
	Type      string `json:"type"`      // "platform-file" | "license-server" | "user-provided"
	EnvVar    string `json:"envVar"`    // env var name injected into the app container
	SecretKey string `json:"secretKey"` // key in the same Secret holding the license value (used by platform-file and license-server only; ignored for user-provided)
	MountPath string `json:"mountPath"` // absolute container path to license file (user-provided only); the file must be mounted by the storage manager
}

// getLicenseConfig reads the license Secret and parses its config.yaml key.
// Returns nil (not error) if secretName is empty or the Secret doesn't exist — license injection is optional.
// Uses controller-runtime's cached client, so this does NOT hit the API server on every call.
func getLicenseConfig(ctx context.Context, k8sClient client.Client, secretName string, namespace string) (*LicenseConfig, error) {
	if secretName == "" {
		return nil, nil
	}

	log := log.FromContext(ctx)

	var secret corev1.Secret
	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: namespace,
	}, &secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("License Secret not found, skipping license injection", "secret", secretName)
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get license Secret %q: %w", secretName, err)
	}

	data, ok := secret.Data["config.yaml"]
	if !ok {
		log.V(1).Info("License Secret has no config.yaml key", "secret", secretName)
		return nil, nil
	}

	var config LicenseConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse license config from Secret %q: %w", secretName, err)
	}

	return &config, nil
}

// injectLicenseEnv returns the env var(s) to inject for a given app, or nil if no license is configured.
// Each app only receives its own license — no cross-contamination between apps.
func injectLicenseEnv(ctx context.Context, appName string, licenseConfig *LicenseConfig, secretName string) []corev1.EnvVar {
	if licenseConfig == nil {
		return nil
	}

	entry, ok := licenseConfig.Licenses[appName]
	if !ok {
		return nil
	}

	if entry.EnvVar == "" {
		log.FromContext(ctx).Info("License entry has empty envVar, skipping",
			"app", appName, "type", entry.Type)
		return nil
	}

	switch entry.Type {
	case "platform-file", "license-server":
		// Both types: value lives in the same license Secret.
		// For platform-file this is the license file content.
		// For license-server this is the connection string (e.g. "27000@host").
		return []corev1.EnvVar{
			{
				Name: entry.EnvVar,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: secretName,
						},
						Key:      entry.SecretKey,
						Optional: ptr.To(true), // Pod starts even if secret/key is missing
					},
				},
			},
		}

	case "user-provided":
		// Point env var at a file path on workspace storage.
		// The file is already mounted into the container via the storage manager.
		if entry.MountPath == "" {
			return nil
		}
		return []corev1.EnvVar{
			{
				Name:  entry.EnvVar,
				Value: entry.MountPath,
			},
		}
	default:
		log.FromContext(ctx).Info("Unknown license type, skipping license injection",
			"app", appName, "type", entry.Type)
	}

	return nil
}
