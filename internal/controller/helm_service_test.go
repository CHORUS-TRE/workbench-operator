package controller

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

var _ = Describe("parseServiceValues", func() {
	It("returns an empty map when Values is nil", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		result, err := parseServiceValues(svc)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	It("returns the parsed map for valid JSON values", func() {
		raw, _ := json.Marshal(map[string]interface{}{
			"storage": map[string]interface{}{"requestedSize": "10Gi"},
		})
		svc := &defaultv1alpha1.WorkspaceService{
			Values: &apiextensionsv1.JSON{Raw: raw},
		}
		result, err := parseServiceValues(svc)
		Expect(err).NotTo(HaveOccurred())
		storage, ok := result["storage"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(storage["requestedSize"]).To(Equal("10Gi"))
	})

	It("returns an error for invalid JSON", func() {
		svc := &defaultv1alpha1.WorkspaceService{
			Values: &apiextensionsv1.JSON{Raw: []byte(`{not valid json}`)},
		}
		_, err := parseServiceValues(svc)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("parsing service values"))
	})
})

var _ = Describe("deleteCredentialSecret", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "del-cred-test-secret"

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("returns false and no error when secret does not exist", func() {
		deleted, err := deleteCredentialSecret(ctx, k8sClient, namespace, "no-such-secret-xyz")
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeFalse())
	})

	It("deletes an existing secret and returns true", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace},
		})).To(Succeed())

		deleted, err := deleteCredentialSecret(ctx, k8sClient, namespace, secretName)
		Expect(err).NotTo(HaveOccurred())
		Expect(deleted).To(BeTrue())

		err = k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, &corev1.Secret{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		Expect(err).To(HaveOccurred()) // should be NotFound
	})
})

var _ = Describe("deleteReleasePVCs", func() {
	ctx := context.Background()
	const namespace = "default"
	const releaseName = "pvc-del-test-release"

	pvcName := "data-" + releaseName + "-0"

	AfterEach(func() {
		pvc := &corev1.PersistentVolumeClaim{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, pvc); err == nil {
			_ = k8sClient.Delete(ctx, pvc)
		}
	})

	It("returns 0 when no PVCs match the release label", func() {
		count, err := deleteReleasePVCs(ctx, k8sClient, namespace, releaseName)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(0))
	})

	It("deletes PVCs with the release instance label and returns the count", func() {
		Expect(k8sClient.Create(ctx, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: namespace,
				Labels:    map[string]string{"app.kubernetes.io/instance": releaseName},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
		})).To(Succeed())

		count, err := deleteReleasePVCs(ctx, k8sClient, namespace, releaseName)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(1))
	})
})

var _ = Describe("reconcileCredentialSecret (missing key in existing secret)", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "partial-data-secret"

	ws := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "partial-key-ws", Namespace: namespace, UID: "uid-partial-key"},
	}

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("skips keys absent from the existing secret's data without error", func() {
		// Pre-create a secret owned by ws that only has "password", not "adminPassword"
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(ws, defaultv1alpha1.GroupVersion.WithKind("Workspace")),
				},
			},
			Data: map[string][]byte{"password": []byte("mypassword")},
		})).To(Succeed())

		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"password", "adminPassword"},
		}
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveKey("password"))
		Expect(result).NotTo(HaveKey("adminPassword"))
	})
})

var _ = Describe("reconcileCredentialSecret (ownership)", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "owned-by-other-ws-secret"

	ownerWorkspace := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-ws", Namespace: namespace, UID: "owner-uid-abc"},
	}
	otherWorkspace := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "other-ws", Namespace: namespace, UID: "other-uid-xyz"},
	}

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("returns an error when the secret is owned by a different workspace", func() {
		// Create a secret owned by ownerWorkspace
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"password"},
		}
		_, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ownerWorkspace, creds)
		Expect(err).NotTo(HaveOccurred())

		// otherWorkspace tries to use the same secret name
		_, err = reconcileCredentialSecret(ctx, k8sClient, namespace, otherWorkspace, creds)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not owned by this Workspace"))
	})
})

var _ = Describe("checkServicePodsHealth", func() {
	ctx := context.Background()
	const namespace = "default"
	const releaseName = "pod-health-test-release"
	label := map[string]string{"app.kubernetes.io/instance": releaseName}

	AfterEach(func() {
		podList := &corev1.PodList{}
		if err := k8sClient.List(ctx, podList, client.InNamespace(namespace), client.MatchingLabels(label)); err == nil {
			for i := range podList.Items {
				_ = k8sClient.Delete(ctx, &podList.Items[i])
			}
		}
	})

	It("returns empty status when no pods exist", func() {
		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(BeEmpty())
		Expect(msg).To(BeEmpty())
	})

	It("returns Failed when a container is in CrashLoopBackOff", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-crash", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-crash", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "db",
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "back-off"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("CrashLoopBackOff"))
	})

	It("returns Failed when a container is in ImagePullBackOff", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-imgpull", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-imgpull", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name: "db",
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("ImagePullBackOff"))
	})

	It("returns Progressing when a container is not ready but not crashing", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-starting", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-starting", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			Ready: false,
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
		Expect(msg).To(ContainSubstring("starting"))
	})

	It("returns empty status when all pods are ready", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-ready", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-ready", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(BeEmpty())
		Expect(msg).To(BeEmpty())
	})

	It("returns Failed when a pod phase is Failed", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-failed", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-failed", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Message = "OOM"
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("failed"))
	})

	It("returns Failed when an init container is in CrashLoopBackOff", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-init-crash", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "init-db", Image: "busybox"}},
				Containers:     []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-init-crash", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "init-db",
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("init container"))
	})

	It("returns Progressing when an init container is waiting but not crashing", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-init-starting", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "init-db", Image: "busybox"}},
				Containers:     []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())

		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-init-starting", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name: "init-db",
			State: corev1.ContainerState{
				Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"},
			},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
		Expect(msg).To(Equal("pods are starting"))
	})
})

var _ = Describe("releaseValuesMatch", func() {
	It("returns true for equal non-empty maps", func() {
		a := map[string]interface{}{"key": "value", "num": float64(1)}
		b := map[string]interface{}{"key": "value", "num": float64(1)}
		Expect(releaseValuesMatch(a, b)).To(BeTrue())
	})

	It("returns false when maps differ", func() {
		a := map[string]interface{}{"key": "value1"}
		b := map[string]interface{}{"key": "value2"}
		Expect(releaseValuesMatch(a, b)).To(BeFalse())
	})

	It("returns true for two nil maps", func() {
		Expect(releaseValuesMatch(nil, nil)).To(BeTrue())
	})

	It("returns false when one map is nil and the other is not", func() {
		Expect(releaseValuesMatch(nil, map[string]interface{}{"k": "v"})).To(BeFalse())
	})

	It("returns true for nested equal maps", func() {
		a := map[string]interface{}{"outer": map[string]interface{}{"inner": "val"}}
		b := map[string]interface{}{"outer": map[string]interface{}{"inner": "val"}}
		Expect(releaseValuesMatch(a, b)).To(BeTrue())
	})
})

var _ = Describe("wrapWithPrefix", func() {
	It("returns values unchanged when prefix is empty", func() {
		vals := map[string]interface{}{"k": "v"}
		result := wrapWithPrefix(vals, "")
		Expect(result).To(Equal(vals))
	})

	It("returns values unchanged when values map is empty", func() {
		result := wrapWithPrefix(map[string]interface{}{}, "myprefix")
		Expect(result).To(BeEmpty())
	})

	It("returns values unchanged when values map is nil", func() {
		Expect(wrapWithPrefix(nil, "myprefix")).To(BeNil())
	})

	It("wraps values under the prefix key", func() {
		vals := map[string]interface{}{"settings": map[string]interface{}{"password": "secret"}}
		result := wrapWithPrefix(vals, "postgres")
		Expect(result).To(HaveKey("postgres"))
		Expect(result["postgres"]).To(Equal(vals))
	})
})

var _ = Describe("validateAppImage", func() {
	It("returns nil when no container runtime is available (best-effort check)", func() {
		ctx := context.Background()
		err := validateAppImage(ctx, "nonexistent-image:latest")
		Expect(err).NotTo(HaveOccurred())
	})
})

var _ = Describe("buildServiceStatus", func() {
	workspace := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "msg-ws", Namespace: "mynamespace"},
	}

	It("populates message from helm description", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "Install complete", svc, "rel", "", workspace)
		Expect(status.Message).To(Equal("Install complete"))
	})

	It("sets empty message when description is empty", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "", svc, "rel", "", workspace)
		Expect(status.Message).To(BeEmpty())
	})

	It("returns Running when deployed and state is Running", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusRunning))
	})

	It("returns Progressing when helm status is pending-install", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-install", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Progressing when helm status is pending-upgrade", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-upgrade", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Progressing when helm status is pending-rollback", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-rollback", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Failed when helm status is failed", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("failed", "install failed", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
	})

	It("returns Stopped when not-found and state is Stopped", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateStopped}
		status := buildServiceStatus("not-found", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusStopped))
	})

	It("returns Deleted when not-found and state is Deleted", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateDeleted}
		status := buildServiceStatus("not-found", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusDeleted))
	})

	It("returns Progressing as default fallback", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("unknown-state", "", svc, "rel", "", workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("renders connection info template when Running", func() {
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "host={{.ReleaseName}}.{{.Namespace}} secret={{.SecretName}}",
		}
		status := buildServiceStatus("deployed", "", svc, "my-release", "my-secret", workspace)
		Expect(status.ConnectionInfo).To(Equal("host=my-release.mynamespace secret=my-secret"))
	})

	It("does not render connection info when not Running", func() {
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "host={{.ReleaseName}}",
		}
		status := buildServiceStatus("failed", "", svc, "my-release", "", workspace)
		Expect(status.ConnectionInfo).To(BeEmpty())
	})
})
