package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("injectLicenseEnv", func() {
	secretName := "app-licenses"
	ctx := context.Background()

	It("returns nil when licenseConfig is nil", func() {
		result := injectLicenseEnv(ctx, "freesurfer", nil, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil when app is not in license config", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
			},
		}
		result := injectLicenseEnv(ctx, "matlab", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns SecretKeyRef env var for platform-file type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
			},
		}
		result := injectLicenseEnv(ctx, "freesurfer", lc, secretName)
		Expect(result).To(HaveLen(1))
		Expect(result[0].Name).To(Equal("FREESURFER_LICENSE"))
		Expect(result[0].ValueFrom).NotTo(BeNil())
		Expect(result[0].ValueFrom.SecretKeyRef.Name).To(Equal("app-licenses"))
		Expect(result[0].ValueFrom.SecretKeyRef.Key).To(Equal("freesurfer"))
		Expect(*result[0].ValueFrom.SecretKeyRef.Optional).To(BeTrue())
	})

	It("returns SecretKeyRef env var for license-server type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"matlab": {Type: "license-server", EnvVar: "MLM_LICENSE_FILE", SecretKey: "matlab"},
			},
		}
		result := injectLicenseEnv(ctx, "matlab", lc, secretName)
		Expect(result).To(HaveLen(1))
		Expect(result[0].Name).To(Equal("MLM_LICENSE_FILE"))
		Expect(result[0].ValueFrom.SecretKeyRef.Name).To(Equal("app-licenses"))
		Expect(result[0].ValueFrom.SecretKeyRef.Key).To(Equal("matlab"))
	})

	It("returns plain Value env var for user-provided type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"someapp": {Type: "user-provided", EnvVar: "APP_LICENSE", MountPath: "/mnt/app_data/license.txt"},
			},
		}
		result := injectLicenseEnv(ctx, "someapp", lc, secretName)
		Expect(result).To(HaveLen(1))
		Expect(result[0].Name).To(Equal("APP_LICENSE"))
		Expect(result[0].Value).To(Equal("/mnt/app_data/license.txt"))
		Expect(result[0].ValueFrom).To(BeNil())
	})

	It("returns nil for user-provided type with empty mountPath", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"someapp": {Type: "user-provided", EnvVar: "APP_LICENSE", MountPath: ""},
			},
		}
		result := injectLicenseEnv(ctx, "someapp", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil for unknown license type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"myapp": {Type: "unknown-type", EnvVar: "SOME_VAR"},
			},
		}
		result := injectLicenseEnv(ctx, "myapp", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil when licenses map is empty", func() {
		lc := &LicenseConfig{Licenses: map[string]LicenseEntry{}}
		result := injectLicenseEnv(ctx, "freesurfer", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("only returns the license for the requested app", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
				"matlab":     {Type: "license-server", EnvVar: "MLM_LICENSE_FILE", SecretKey: "matlab"},
			},
		}
		result := injectLicenseEnv(ctx, "freesurfer", lc, secretName)
		Expect(result).To(HaveLen(1))
		Expect(result[0].Name).To(Equal("FREESURFER_LICENSE"))
	})
})

var _ = Describe("getLicenseConfig", func() {
	const namespace = "default"
	ctx := context.Background()

	It("returns nil when secretName is empty", func() {
		cfg, err := getLicenseConfig(ctx, k8sClient, "", namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(BeNil())
	})

	It("returns nil when Secret does not exist (NotFound)", func() {
		cfg, err := getLicenseConfig(ctx, k8sClient, "nonexistent-secret", namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(BeNil())
	})

	It("returns parsed config when Secret has valid config.yaml", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "license-valid",
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"config.yaml": []byte(`licenses:
  freesurfer:
    type: platform-file
    envVar: FREESURFER_LICENSE
    secretKey: freesurfer
`),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

		cfg, err := getLicenseConfig(ctx, k8sClient, "license-valid", namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Licenses).To(HaveKey("freesurfer"))
		Expect(cfg.Licenses["freesurfer"].Type).To(Equal("platform-file"))
		Expect(cfg.Licenses["freesurfer"].EnvVar).To(Equal("FREESURFER_LICENSE"))
		Expect(cfg.Licenses["freesurfer"].SecretKey).To(Equal("freesurfer"))
	})

	It("returns nil when Secret exists but has no config.yaml key", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "license-no-config-key",
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"other-key": []byte("some data"),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

		cfg, err := getLicenseConfig(ctx, k8sClient, "license-no-config-key", namespace)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(BeNil())
	})

	It("returns error when config.yaml contains invalid YAML", func() {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "license-bad-yaml",
				Namespace: namespace,
			},
			Data: map[string][]byte{
				"config.yaml": []byte(`licenses: [invalid yaml`),
			},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, secret) })

		cfg, err := getLicenseConfig(ctx, k8sClient, "license-bad-yaml", namespace)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("failed to parse license config"))
		Expect(cfg).To(BeNil())
	})
})
