package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// helper to build a minimal StorageManager backed by testenv
func newTestStorageManager(cfg Config) *StorageManager {
	return NewStorageManager(newTestReconciler(cfg))
}

// helper to build a minimal Workbench for initJob tests
func makeWorkbench(ns, name string) defaultv1alpha1.Workbench {
	return defaultv1alpha1.Workbench{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: defaultv1alpha1.WorkbenchSpec{
			Server: defaultv1alpha1.WorkbenchServer{
				User:   "alice",
				UserID: 1234,
			},
		},
	}
}

// helper to build a minimal Service for initJob tests
func makeService(ns, name string) corev1.Service {
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

// helper to build a minimal WorkbenchApp with an image
func makeApp(name, repo, tag string) defaultv1alpha1.WorkbenchApp {
	return defaultv1alpha1.WorkbenchApp{
		Name: name,
		Image: defaultv1alpha1.Image{
			Repository: repo,
			Tag:        tag,
		},
	}
}

var _ = Describe("initJob", func() {
	ctx := context.Background()

	wb := makeWorkbench("ns1", "wb1")
	svc := makeService("ns1", "wb1")

	It("returns an error when app has no image (appName empty)", func() {
		app := defaultv1alpha1.WorkbenchApp{Name: "myapp"} // no image set
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("app.Image is required"))
		Expect(job).To(BeNil())
	})

	It("sets image pull policy to PullAlways when tag is empty", func() {
		app := makeApp("myapp", "apps/myapp", "")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring(":latest"))
	})

	It("sets image pull policy to PullIfNotPresent when tag is set", func() {
		app := makeApp("myapp", "apps/myapp", "v1.2.3")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(ContainSubstring("v1.2.3"))
	})

	It("uses config Registry when app image registry is empty", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		cfg := Config{Registry: "registry.example.com"}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("registry.example.com/"))
	})

	It("uses app.Image.Registry when set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		app.Image.Registry = "custom.registry.io"
		cfg := Config{Registry: "should-not-be-used.io"}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("custom.registry.io/"))
	})

	It("returns error when app.Image.Repository is empty (appName cannot be derived)", func() {
		app := defaultv1alpha1.WorkbenchApp{
			Name:  "freesurfer",
			Image: defaultv1alpha1.Image{Tag: "v7"},
			// Repository intentionally empty
		}
		cfg := Config{AppsRepository: "harbor.example.com/apps"}
		sm := newTestStorageManager(cfg)
		_, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("app.Image is required"))
	})

	It("adds shm volume and mount when ShmSize is set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		shmSize := resource.MustParse("256Mi")
		app.ShmSize = &shmSize
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		// Check shm volume exists
		var shmVol *corev1.Volume
		for i := range job.Spec.Template.Spec.Volumes {
			if job.Spec.Template.Spec.Volumes[i].Name == "shm" {
				shmVol = &job.Spec.Template.Spec.Volumes[i]
				break
			}
		}
		Expect(shmVol).NotTo(BeNil())
		Expect(shmVol.EmptyDir).NotTo(BeNil())
		Expect(string(shmVol.EmptyDir.Medium)).To(Equal("Memory"))
		// Check mount in main container
		var shmMount *corev1.VolumeMount
		for i := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
			if job.Spec.Template.Spec.Containers[0].VolumeMounts[i].MountPath == "/dev/shm" {
				shmMount = &job.Spec.Template.Spec.Containers[0].VolumeMounts[i]
				break
			}
		}
		Expect(shmMount).NotTo(BeNil())
	})

	It("does not add shm volume when ShmSize is nil", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		for _, vol := range job.Spec.Template.Spec.Volumes {
			Expect(vol.Name).NotTo(Equal("shm"))
		}
	})

	It("sets service account name when Workbench.Spec.ServiceAccount is set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		wbWithSA := makeWorkbench("ns1", "wb1")
		wbWithSA.Spec.ServiceAccount = "my-sa"
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wbWithSA, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("my-sa"))
	})

	It("does not set service account name when empty", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.ServiceAccountName).To(BeEmpty())
	})

	It("sets priority class when ApplicationPriorityClassName is set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		cfg := Config{ApplicationPriorityClassName: "high-priority"}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.PriorityClassName).To(Equal("high-priority"))
	})

	It("adds debug annotations when DebugModeEnabled is true", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		cfg := Config{DebugModeEnabled: true}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Annotations).To(HaveKey("chorus-tre.ch/debug-mode"))
		Expect(job.Spec.Template.Annotations).To(HaveKey("chorus-tre.ch/debug-warning"))
	})

	It("suspends the job when app.State is not Running", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		app.State = "Stopped"
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Suspend).NotTo(BeNil())
		Expect(*job.Spec.Suspend).To(BeTrue())
	})

	It("does not suspend the job when app.State is Running", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		app.State = "Running"
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Suspend).NotTo(BeNil())
		Expect(*job.Spec.Suspend).To(BeFalse())
	})

	It("does not suspend the job when app.State is empty (defaults to Running)", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(*job.Spec.Suspend).To(BeFalse())
	})

	It("overrides resources when app.Resources.Limits is set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		customLimits := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
		app.Resources = &corev1.ResourceRequirements{
			Limits: customLimits,
		}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("2"))
	})

	It("uses requests as limits when only requests are set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		customReqs := corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		}
		app.Resources = &corev1.ResourceRequirements{
			Requests: customReqs,
		}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		// When only requests given, limits should equal requests
		Expect(job.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("500m"))
		Expect(job.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("500m"))
	})

	It("uses config InitContainerImage when set", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		cfg := Config{InitContainerImage: "my.registry.io/init-container"}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		initContainerImage := job.Spec.Template.Spec.InitContainers[0].Image
		Expect(initContainerImage).To(HavePrefix("my.registry.io/init-container:"))
	})

	It("falls back to registry-based init container image when InitContainerImage is empty", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		cfg := Config{Registry: "registry.example.com"}
		sm := newTestStorageManager(cfg)
		job, err := initJob(ctx, wb, cfg, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		initContainerImage := job.Spec.Template.Spec.InitContainers[0].Image
		Expect(initContainerImage).To(HavePrefix("registry.example.com/apps/app-init:"))
	})

	It("sets init container pull policy to PullAlways when version is latest", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		// Default initContainerVersion is "latest" → PullAlways
		Expect(job.Spec.Template.Spec.InitContainers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
	})

	It("sets init container pull policy to PullIfNotPresent when version is pinned", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		wbWithInit := makeWorkbench("ns1", "wb1")
		wbWithInit.Spec.InitContainer = &defaultv1alpha1.InitContainerConfig{Version: "v2.3.4"}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wbWithInit, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.InitContainers[0].ImagePullPolicy).To(Equal(corev1.PullIfNotPresent))
	})

	It("appends image pull secrets", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		wbWithSecrets := makeWorkbench("ns1", "wb1")
		wbWithSecrets.Spec.ImagePullSecrets = []string{"regcred", "harbor-creds"}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wbWithSecrets, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))
		Expect(job.Spec.Template.Spec.ImagePullSecrets[0].Name).To(Equal("regcred"))
		Expect(job.Spec.Template.Spec.ImagePullSecrets[1].Name).To(Equal("harbor-creds"))
	})

	It("sets TTLSecondsAfterFinished to 1 day", func() {
		app := makeApp("myapp", "apps/myapp", "v1")
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		Expect(job.Spec.TTLSecondsAfterFinished).NotTo(BeNil())
		Expect(*job.Spec.TTLSecondsAfterFinished).To(Equal(int32(24 * 3600)))
	})

	It("adds KIOSK_URL env when app is kiosk and KioskConfig is set", func() {
		app := makeApp("kiosk", "apps/kiosk", "v1")
		app.KioskConfig = &defaultv1alpha1.KioskConfig{
			URL: "https://example.com",
		}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		var found bool
		for _, env := range job.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "KIOSK_URL" {
				Expect(env.Value).To(Equal("https://example.com"))
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())
	})

	It("adds KIOSK_JWT env vars when KioskConfig has JWT URL and token", func() {
		jwtURL := "https://jwt.example.com"
		jwtToken := "mytoken"
		app := makeApp("kiosk", "apps/kiosk", "v1")
		app.KioskConfig = &defaultv1alpha1.KioskConfig{
			URL:      "https://example.com",
			JWTURL:   &jwtURL,
			JWTToken: &jwtToken,
		}
		sm := newTestStorageManager(Config{})
		job, err := initJob(ctx, wb, Config{}, "uid1", app, svc, sm)
		Expect(err).NotTo(HaveOccurred())
		envMap := make(map[string]string)
		for _, env := range job.Spec.Template.Spec.Containers[0].Env {
			envMap[env.Name] = env.Value
		}
		Expect(envMap).To(HaveKey("KIOSK_JWT_URL"))
		Expect(envMap["KIOSK_JWT_URL"]).To(Equal(jwtURL))
		Expect(envMap).To(HaveKey("KIOSK_JWT_TOKEN"))
		Expect(envMap["KIOSK_JWT_TOKEN"]).To(Equal(jwtToken))
	})
})

var _ = Describe("updateJob", func() {
	makeSuspendedJob := func(suspended bool) batchv1.Job {
		return batchv1.Job{
			Spec: batchv1.JobSpec{
				Suspend: &suspended,
			},
		}
	}

	It("returns false when suspend state is identical", func() {
		src := makeSuspendedJob(true)
		dst := makeSuspendedJob(true)
		Expect(updateJob(src, &dst)).To(BeFalse())
	})

	It("returns true and updates when suspend state differs", func() {
		src := makeSuspendedJob(true)
		dst := makeSuspendedJob(false)
		Expect(updateJob(src, &dst)).To(BeTrue())
		Expect(*dst.Spec.Suspend).To(BeTrue())
	})

	It("returns true and sets suspend when destination has nil suspend", func() {
		src := makeSuspendedJob(false)
		dst := batchv1.Job{}
		Expect(updateJob(src, &dst)).To(BeTrue())
		Expect(*dst.Spec.Suspend).To(BeFalse())
	})

	It("returns false when source has nil suspend", func() {
		src := batchv1.Job{} // nil suspend
		dst := makeSuspendedJob(true)
		Expect(updateJob(src, &dst)).To(BeFalse())
	})
})
