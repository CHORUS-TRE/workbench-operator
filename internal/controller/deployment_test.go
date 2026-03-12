package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

var _ = Describe("buildSecurityContext", func() {
	It("returns nil in non-debug mode", func() {
		ctx := buildSecurityContext(false)
		Expect(ctx).To(BeNil())
	})

	It("returns elevated security context in debug mode", func() {
		ctx := buildSecurityContext(true)
		Expect(ctx).NotTo(BeNil())
		Expect(*ctx.RunAsUser).To(Equal(int64(0)))
		Expect(*ctx.RunAsGroup).To(Equal(int64(0)))
		Expect(*ctx.Privileged).To(BeTrue())
		Expect(*ctx.AllowPrivilegeEscalation).To(BeTrue())
		Expect(*ctx.RunAsNonRoot).To(BeFalse())
	})
})

var _ = Describe("buildAppSecurityContext", func() {
	It("returns elevated security context in debug mode", func() {
		ctx := buildAppSecurityContext(true, 1000, 1000)
		Expect(ctx).NotTo(BeNil())
		Expect(*ctx.RunAsUser).To(Equal(int64(0)))
		Expect(*ctx.Privileged).To(BeTrue())
	})

	It("returns restricted security context in normal mode", func() {
		ctx := buildAppSecurityContext(false, 1234, 5678)
		Expect(ctx).NotTo(BeNil())
		Expect(*ctx.RunAsUser).To(Equal(int64(1234)))
		Expect(*ctx.RunAsGroup).To(Equal(int64(5678)))
		Expect(*ctx.RunAsNonRoot).To(BeTrue())
		Expect(*ctx.AllowPrivilegeEscalation).To(BeFalse())
		Expect(ctx.Capabilities).NotTo(BeNil())
		Expect(ctx.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")))
	})
})

var _ = Describe("getAppName", func() {
	It("returns empty string when repository is empty", func() {
		app := defaultv1alpha1.WorkbenchApp{}
		Expect(getAppName(app)).To(BeEmpty())
	})

	It("returns the last path component of the repository", func() {
		app := defaultv1alpha1.WorkbenchApp{
			Image: defaultv1alpha1.Image{Repository: "apps/freesurfer"},
		}
		Expect(getAppName(app)).To(Equal("freesurfer"))
	})

	It("returns the repository itself when no slash is present", func() {
		app := defaultv1alpha1.WorkbenchApp{
			Image: defaultv1alpha1.Image{Repository: "myapp"},
		}
		Expect(getAppName(app)).To(Equal("myapp"))
	})

	It("handles deeply nested repository paths", func() {
		app := defaultv1alpha1.WorkbenchApp{
			Image: defaultv1alpha1.Image{Repository: "org/team/apps/myapp"},
		}
		Expect(getAppName(app)).To(Equal("myapp"))
	})
})

var _ = Describe("updateDeployment", func() {
	makeDeployment := func(mainImage, sidecarImage string) appsv1.Deployment {
		return appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						InitContainers: []corev1.Container{{Name: "sidecar", Image: sidecarImage}},
						Containers:     []corev1.Container{{Name: "main", Image: mainImage}},
					},
				},
			},
		}
	}

	It("returns false when images are already identical", func() {
		src := makeDeployment("img:v1", "side:v1")
		dst := makeDeployment("img:v1", "side:v1")
		Expect(updateDeployment(src, &dst)).To(BeFalse())
	})

	It("updates and returns true when main container image differs", func() {
		src := makeDeployment("img:v2", "side:v1")
		dst := makeDeployment("img:v1", "side:v1")
		Expect(updateDeployment(src, &dst)).To(BeTrue())
		Expect(dst.Spec.Template.Spec.Containers[0].Image).To(Equal("img:v2"))
	})

	It("updates and returns true when sidecar image differs", func() {
		src := makeDeployment("img:v1", "side:v2")
		dst := makeDeployment("img:v1", "side:v1")
		Expect(updateDeployment(src, &dst)).To(BeTrue())
		Expect(dst.Spec.Template.Spec.InitContainers[0].Image).To(Equal("side:v2"))
	})

	It("replaces containers entirely when destination has extra containers", func() {
		src := makeDeployment("img:v1", "side:v1")
		dst := appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						InitContainers: []corev1.Container{{Name: "sidecar", Image: "side:v1"}},
						Containers: []corev1.Container{
							{Name: "main", Image: "img:v1"},
							{Name: "extra", Image: "extra:v1"},
						},
					},
				},
			},
		}
		Expect(updateDeployment(src, &dst)).To(BeTrue())
		Expect(dst.Spec.Template.Spec.Containers).To(HaveLen(1))
	})

	It("replaces init containers entirely when destination has extra init containers", func() {
		src := makeDeployment("img:v1", "side:v1")
		dst := appsv1.Deployment{
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						InitContainers: []corev1.Container{
							{Name: "sidecar", Image: "side:v1"},
							{Name: "extra-init", Image: "extra-init:v1"},
						},
						Containers: []corev1.Container{{Name: "main", Image: "img:v1"}},
					},
				},
			},
		}
		Expect(updateDeployment(src, &dst)).To(BeTrue())
		Expect(dst.Spec.Template.Spec.InitContainers).To(HaveLen(1))
	})
})

var _ = Describe("initDeployment", func() {
	workbench := defaultv1alpha1.Workbench{
		ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "ns1"},
	}

	It("includes debug mode annotations when DebugModeEnabled", func() {
		cfg := Config{DebugModeEnabled: true}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.Template.Annotations).To(HaveKey("chorus-tre.ch/debug-mode"))
	})

	It("does not include debug annotations in normal mode", func() {
		cfg := Config{}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.Template.Annotations).NotTo(HaveKey("chorus-tre.ch/debug-mode"))
	})

	It("sets service account name when specified", func() {
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb2", Namespace: "ns2"},
			Spec:       defaultv1alpha1.WorkbenchSpec{ServiceAccount: "my-sa"},
		}
		d := initDeployment(wb, Config{})
		Expect(d.Spec.Template.Spec.ServiceAccountName).To(Equal("my-sa"))
	})

	It("sets priority class name when configured", func() {
		cfg := Config{WorkbenchPriorityClassName: "high-priority"}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.Template.Spec.PriorityClassName).To(Equal("high-priority"))
	})

	It("sets ProgressDeadlineSeconds when WorkbenchStartupTimeout > 0", func() {
		cfg := Config{WorkbenchStartupTimeout: 120}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.ProgressDeadlineSeconds).NotTo(BeNil())
		Expect(*d.Spec.ProgressDeadlineSeconds).To(Equal(int32(120)))
	})

	It("does not set ProgressDeadlineSeconds when WorkbenchStartupTimeout is 0", func() {
		cfg := Config{WorkbenchStartupTimeout: 0}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.ProgressDeadlineSeconds).To(BeNil())
	})

	It("adds INITIAL_RESOLUTION env when both dimensions are set", func() {
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb3", Namespace: "ns3"},
			Spec: defaultv1alpha1.WorkbenchSpec{
				Server: defaultv1alpha1.WorkbenchServer{
					InitialResolutionWidth:  1920,
					InitialResolutionHeight: 1080,
				},
			},
		}
		d := initDeployment(wb, Config{})
		var found bool
		for _, env := range d.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "INITIAL_RESOLUTION" {
				Expect(env.Value).To(Equal("1920x1080"))
				found = true
				break
			}
		}
		Expect(found).To(BeTrue())
	})

	It("does not add INITIAL_RESOLUTION env when dimensions are zero", func() {
		d := initDeployment(workbench, Config{})
		for _, env := range d.Spec.Template.Spec.Containers[0].Env {
			Expect(env.Name).NotTo(Equal("INITIAL_RESOLUTION"))
		}
	})

	It("uses WorkbenchDefaultResources from config when Server.Resources is nil", func() {
		cpuLimit := resource.MustParse("500m")
		memLimit := resource.MustParse("1Gi")
		cfg := Config{
			WorkbenchDefaultResources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    cpuLimit,
					corev1.ResourceMemory: memLimit,
				},
			},
		}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("500m"))
	})

	It("prefers Server.Resources over config defaults", func() {
		cpuFromSpec := resource.MustParse("2")
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb4", Namespace: "ns4"},
			Spec: defaultv1alpha1.WorkbenchSpec{
				Server: defaultv1alpha1.WorkbenchServer{
					Resources: &corev1.ResourceRequirements{
						Limits: corev1.ResourceList{corev1.ResourceCPU: cpuFromSpec},
					},
				},
			},
		}
		cpuFromConfig := resource.MustParse("100m")
		cfg := Config{
			WorkbenchDefaultResources: &corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceCPU: cpuFromConfig},
			},
		}
		d := initDeployment(wb, cfg)
		Expect(d.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("2"))
	})

	It("uses XpraServerImage from config when set", func() {
		cfg := Config{XpraServerImage: "myregistry.io/xpra-server"}
		d := initDeployment(workbench, Config{XpraServerImage: "myregistry.io/xpra-server"})
		_ = cfg
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("myregistry.io/xpra-server:"))
	})

	It("falls back to registry-based xpra server image when XpraServerImage is empty", func() {
		cfg := Config{Registry: "my.registry.io", AppsRepository: "apps"}
		d := initDeployment(workbench, cfg)
		Expect(d.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my.registry.io/apps/xpra-server:"))
	})
})

var _ = Describe("determineAppContainerMessage", func() {
	var r *WorkbenchReconciler

	BeforeEach(func() {
		r = &WorkbenchReconciler{
			Client:   k8sClient,
			Scheme:   k8sClient.Scheme(),
			Recorder: record.NewFakeRecorder(10),
		}
	})

	It("returns Waiting message without detail when Waiting.Message is empty", func() {
		cs := &corev1.ContainerStatus{
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
			},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Waiting: ContainerCreating"))
	})

	It("returns Waiting message with detail when Waiting.Message is set", func() {
		cs := &corev1.ContainerStatus{
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CrashLoopBackOff",
					Message: "back-off 5m0s restarting failed container",
				},
			},
		}
		msg := r.determineAppContainerMessage(cs)
		Expect(msg).To(ContainSubstring("CrashLoopBackOff"))
		Expect(msg).To(ContainSubstring("back-off 5m0s restarting failed container"))
	})

	It("returns Terminated message without detail when Terminated.Message is empty", func() {
		cs := &corev1.ContainerStatus{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:   "OOMKilled",
					ExitCode: 137,
				},
			},
		}
		msg := r.determineAppContainerMessage(cs)
		Expect(msg).To(ContainSubstring("137"))
		Expect(msg).To(ContainSubstring("OOMKilled"))
		Expect(msg).NotTo(ContainSubstring(" - "))
	})

	It("returns Terminated message with detail when Terminated.Message is set", func() {
		cs := &corev1.ContainerStatus{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{
					Reason:   "Error",
					ExitCode: 1,
					Message:  "process exited",
				},
			},
		}
		msg := r.determineAppContainerMessage(cs)
		Expect(msg).To(ContainSubstring("1"))
		Expect(msg).To(ContainSubstring("Error"))
		Expect(msg).To(ContainSubstring("process exited"))
	})

	It("returns unknown when Running is nil and Waiting/Terminated are also nil", func() {
		cs := &corev1.ContainerStatus{
			State: corev1.ContainerState{},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Container state unknown"))
	})

	It("returns ready message when container is Running and Ready", func() {
		cs := &corev1.ContainerStatus{
			Ready: true,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
			},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Container is ready"))
	})

	It("returns starting up when container is Running, not Ready, restartCount=0, and recently started", func() {
		cs := &corev1.ContainerStatus{
			Ready:        false,
			RestartCount: 0,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
			},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Container starting up"))
	})

	It("returns readiness probe failing when container is Running, not Ready, and been running > 2 minutes", func() {
		cs := &corev1.ContainerStatus{
			Ready:        false,
			RestartCount: 0,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{
					StartedAt: metav1.NewTime(time.Now().Add(-3 * time.Minute)),
				},
			},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Readiness probe failing"))
	})

	It("returns readiness probe failing when restartCount > 0 even if recently started", func() {
		cs := &corev1.ContainerStatus{
			Ready:        false,
			RestartCount: 1,
			State: corev1.ContainerState{
				Running: &corev1.ContainerStateRunning{StartedAt: metav1.Now()},
			},
		}
		Expect(r.determineAppContainerMessage(cs)).To(Equal("Readiness probe failing"))
	})
})
