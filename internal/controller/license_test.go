package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("injectLicenseEnv", func() {
	secretName := "app-licenses"

	It("returns nil when licenseConfig is nil", func() {
		result := injectLicenseEnv("freesurfer", nil, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil when app is not in license config", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
			},
		}
		result := injectLicenseEnv("matlab", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns SecretKeyRef env var for platform-file type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
			},
		}
		result := injectLicenseEnv("freesurfer", lc, secretName)
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
		result := injectLicenseEnv("matlab", lc, secretName)
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
		result := injectLicenseEnv("someapp", lc, secretName)
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
		result := injectLicenseEnv("someapp", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil for unknown license type", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"myapp": {Type: "unknown-type", EnvVar: "SOME_VAR"},
			},
		}
		result := injectLicenseEnv("myapp", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("returns nil when licenses map is empty", func() {
		lc := &LicenseConfig{Licenses: map[string]LicenseEntry{}}
		result := injectLicenseEnv("freesurfer", lc, secretName)
		Expect(result).To(BeNil())
	})

	It("only returns the license for the requested app", func() {
		lc := &LicenseConfig{
			Licenses: map[string]LicenseEntry{
				"freesurfer": {Type: "platform-file", EnvVar: "FREESURFER_LICENSE", SecretKey: "freesurfer"},
				"matlab":     {Type: "license-server", EnvVar: "MLM_LICENSE_FILE", SecretKey: "matlab"},
			},
		}
		result := injectLicenseEnv("freesurfer", lc, secretName)
		Expect(result).To(HaveLen(1))
		Expect(result[0].Name).To(Equal("FREESURFER_LICENSE"))
	})
})
