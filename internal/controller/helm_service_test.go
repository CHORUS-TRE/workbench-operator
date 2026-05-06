package controller

import (
	"context"
	"encoding/json"
	"io"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart"
	chartcommon "helm.sh/helm/v4/pkg/chart/common"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

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

var _ = Describe("reconcileCredentialSecret (nil creds)", func() {
	It("returns nil, nil when creds is nil", func() {
		result, err := reconcileCredentialSecret(context.Background(), k8sClient, "default", &defaultv1alpha1.Workspace{}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeNil())
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

var _ = Describe("reconcileCredentialSecret (multiple independent paths)", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "multi-path-cred-secret"

	ws := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "multi-path-ws", Namespace: namespace, UID: "uid-multi-path"},
	}

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("generates a distinct password for each path and injects them into helm values", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"db.password", "admin.password"},
		}
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey("db.password"))
		Expect(secret.Data).To(HaveKey("admin.password"))
		Expect(string(secret.Data["db.password"])).NotTo(Equal(string(secret.Data["admin.password"])))

		Expect(result).To(HaveKeyWithValue("db", HaveKeyWithValue("password", string(secret.Data["db.password"]))))
		Expect(result).To(HaveKeyWithValue("admin", HaveKeyWithValue("password", string(secret.Data["admin.password"]))))
	})

	It("returns empty helm values when Paths is empty", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{},
		}
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
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

	It("deletes multiple PVCs and returns the correct count", func() {
		const multiRelease = "pvc-del-multi-release"
		pvcNames := []string{"data-" + multiRelease + "-0", "data-" + multiRelease + "-1"}
		for _, name := range pvcNames {
			Expect(k8sClient.Create(ctx, &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: namespace,
					Labels:    map[string]string{"app.kubernetes.io/instance": multiRelease},
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
		}

		count, err := deleteReleasePVCs(ctx, k8sClient, namespace, multiRelease)
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(2))
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

	It("returns an error when a key listed in paths is absent from the existing secret", func() {
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
		_, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).To(MatchError(ContainSubstring("adminPassword")))
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

var _ = Describe("reconcileCredentialSecret (path aliases)", func() {
	ctx := context.Background()
	const namespace = "default"
	const secretName = "alias-cred-secret"

	ws := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "alias-ws", Namespace: namespace, UID: "uid-alias"},
	}

	AfterEach(func() {
		secret := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret); err == nil {
			_ = k8sClient.Delete(ctx, secret)
		}
	})

	It("injects one password at multiple helm paths and stores it under the primary key", func() {
		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"a.b.value|x.y.password"},
		}
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())

		// Secret stores password under primary key only
		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey("a.b.value"))
		Expect(secret.Data).NotTo(HaveKey("x.y.password"))

		// Helm values injected at both paths with the same value
		pw := string(secret.Data["a.b.value"])
		Expect(result).To(HaveKeyWithValue("a", HaveKeyWithValue("b", HaveKeyWithValue("value", pw))))
		Expect(result).To(HaveKeyWithValue("x", HaveKeyWithValue("y", HaveKeyWithValue("password", pw))))
	})

	It("reads back an existing secret and injects all alias paths", func() {
		// Pre-create the secret with only the primary key (as it would exist after first reconcile)
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(ws, defaultv1alpha1.GroupVersion.WithKind("Workspace")),
				},
			},
			Data: map[string][]byte{"a.b.value": []byte("existingpw")},
		})).To(Succeed())

		creds := &defaultv1alpha1.WorkspaceServiceCredentials{
			SecretName: secretName,
			Paths:      []string{"a.b.value|x.y.password"},
		}
		result, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
		Expect(err).NotTo(HaveOccurred())

		// Both alias paths receive the value from the existing secret
		Expect(result).To(HaveKeyWithValue("a", HaveKeyWithValue("b", HaveKeyWithValue("value", "existingpw"))))
		Expect(result).To(HaveKeyWithValue("x", HaveKeyWithValue("y", HaveKeyWithValue("password", "existingpw"))))
	})
})

var _ = Describe("reconcileCredentialSecret (path validation)", func() {
	ctx := context.Background()
	const namespace = "default"

	ws := &defaultv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "validation-ws", Namespace: namespace, UID: "uid-validation"},
	}

	DescribeTable("returns an error for malformed path entries",
		func(path string) {
			creds := &defaultv1alpha1.WorkspaceServiceCredentials{
				SecretName: "validation-secret",
				Paths:      []string{path},
			}
			_, err := reconcileCredentialSecret(ctx, k8sClient, namespace, ws, creds)
			Expect(err).To(MatchError(ContainSubstring("empty segment")))
		},
		Entry("trailing pipe", "a.b|"),
		Entry("leading pipe", "|a.b"),
		Entry("double pipe", "a.b||c.d"),
	)
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
		Expect(msg).To(ContainSubstring("Init container"))
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
		Expect(msg).To(Equal("Pods are starting"))
	})

	It("returns Failed when a container is in OOMKilled", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-oom", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}}},
		})).To(Succeed())
		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-oom", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "OOMKilled"}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("OOMKilled"))
	})

	It("returns Failed when a container is in ErrImagePull", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-errimgpull", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}}},
		})).To(Succeed())
		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-errimgpull", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ErrImagePull"}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("ErrImagePull"))
	})

	It("returns Failed when an init container is in OOMKilled", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-init-oom", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{
				InitContainers: []corev1.Container{{Name: "init-db", Image: "busybox"}},
				Containers:     []corev1.Container{{Name: "db", Image: "postgres:15"}},
			},
		})).To(Succeed())
		pod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-init-oom", Namespace: namespace}, pod)).To(Succeed())
		pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
			Name:  "init-db",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "OOMKilled"}},
		}}
		Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

		status, msg := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
		Expect(msg).To(ContainSubstring("Init container"))
		Expect(msg).To(ContainSubstring("OOMKilled"))
	})

	It("returns Failed when one pod is crashing and another is ready", func() {
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-multi-crash", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}}},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: releaseName + "-pod-multi-ready", Namespace: namespace, Labels: label,
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "db", Image: "postgres:15"}}},
		})).To(Succeed())

		crashPod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-multi-crash", Namespace: namespace}, crashPod)).To(Succeed())
		crashPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		}}
		Expect(k8sClient.Status().Update(ctx, crashPod)).To(Succeed())

		readyPod := &corev1.Pod{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: releaseName + "-pod-multi-ready", Namespace: namespace}, readyPod)).To(Succeed())
		readyPod.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:  "db",
			Ready: true,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
		}}
		Expect(k8sClient.Status().Update(ctx, readyPod)).To(Succeed())

		status, _ := checkServicePodsHealth(ctx, k8sClient, namespace, releaseName)
		Expect(status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
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
		status := buildServiceStatus("deployed", "Install complete", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Message).To(Equal("Install complete"))
	})

	It("sets empty message when description is empty", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Message).To(BeEmpty())
	})

	It("returns Running when deployed and state is Running", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusRunning))
	})

	It("returns Progressing when helm status is pending-install", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-install", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Progressing when helm status is pending-upgrade", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-upgrade", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Progressing when helm status is pending-rollback", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("pending-rollback", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Failed when helm status is failed", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("failed", "install failed", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusFailed))
	})

	It("returns Stopped when not-found and state is Stopped", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateStopped}
		status := buildServiceStatus("not-found", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusStopped))
	})

	It("returns Deleted when not-found and state is Deleted", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateDeleted}
		status := buildServiceStatus("not-found", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusDeleted))
	})

	It("returns Progressing as default fallback", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("unknown-state", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("renders connection info template when Running", func() {
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "host={{.ReleaseName}}.{{.Namespace}} secret={{.SecretName}}",
		}
		status := buildServiceStatus("deployed", "", svc, "my-release", "my-secret", svc.ConnectionInfoTemplate, workspace)
		Expect(status.ConnectionInfo).To(Equal("host=my-release.mynamespace secret=my-secret"))
	})

	It("uses the explicit template parameter, not svc.ConnectionInfoTemplate", func() {
		// Lock in the contract: the function reads the connectionInfoTemplate parameter
		// (which the controller resolves from CR + chorus.yaml fallback) and ignores
		// the field on the WorkspaceService struct.
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "from-svc-{{.ReleaseName}}",
		}
		status := buildServiceStatus("deployed", "", svc, "my-release", "", "from-param-{{.ReleaseName}}", workspace)
		Expect(status.ConnectionInfo).To(Equal("from-param-my-release"))
	})

	It("renders an empty connection info when the template parameter is empty even if svc has one", func() {
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "from-svc-{{.ReleaseName}}",
		}
		status := buildServiceStatus("deployed", "", svc, "my-release", "", "", workspace)
		Expect(status.ConnectionInfo).To(BeEmpty())
	})

	It("does not render connection info when not Running", func() {
		svc := defaultv1alpha1.WorkspaceService{
			State:                  defaultv1alpha1.WorkspaceServiceStateRunning,
			ConnectionInfoTemplate: "host={{.ReleaseName}}",
		}
		status := buildServiceStatus("failed", "", svc, "my-release", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.ConnectionInfo).To(BeEmpty())
	})

	It("populates SecretName in status", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateRunning}
		status := buildServiceStatus("deployed", "", svc, "rel", "my-creds", svc.ConnectionInfoTemplate, workspace)
		Expect(status.SecretName).To(Equal("my-creds"))
	})

	It("populates SecretName even when not Running", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateStopped}
		status := buildServiceStatus("not-found", "", svc, "rel", "my-creds", svc.ConnectionInfoTemplate, workspace)
		Expect(status.SecretName).To(Equal("my-creds"))
	})

	It("returns Progressing when deployed but desired state is Stopped", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateStopped}
		status := buildServiceStatus("deployed", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})

	It("returns Progressing when deployed but desired state is Deleted", func() {
		svc := defaultv1alpha1.WorkspaceService{State: defaultv1alpha1.WorkspaceServiceStateDeleted}
		status := buildServiceStatus("deployed", "", svc, "rel", "", svc.ConnectionInfoTemplate, workspace)
		Expect(status.Status).To(Equal(defaultv1alpha1.WorkspaceStatusServiceStatusProgressing))
	})
})

var _ = Describe("evaluateComputedValues", func() {
	It("returns an empty map when computedValues is nil", func() {
		result, err := evaluateComputedValues(nil, "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	It("returns an empty map when computedValues is empty", func() {
		result, err := evaluateComputedValues(map[string]string{}, "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	It("substitutes {{.ReleaseName}} in a value", func() {
		result, err := evaluateComputedValues(
			map[string]string{"mlflow.backendStore.postgres.host": "{{.ReleaseName}}-mlflow-db"},
			"workspace156-mlflow", "workspace156", "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveKeyWithValue("mlflow",
			HaveKeyWithValue("backendStore",
				HaveKeyWithValue("postgres",
					HaveKeyWithValue("host", "workspace156-mlflow-mlflow-db")))))
	})

	It("substitutes {{.Namespace}} in a value", func() {
		result, err := evaluateComputedValues(
			map[string]string{"app.namespace": "{{.Namespace}}"},
			"rel", "mynamespace", "",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveKeyWithValue("app", HaveKeyWithValue("namespace", "mynamespace")))
	})

	It("substitutes {{.SecretName}} in a value", func() {
		result, err := evaluateComputedValues(
			map[string]string{"app.secret": "{{.SecretName}}"},
			"rel", "ns", "my-secret",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveKeyWithValue("app", HaveKeyWithValue("secret", "my-secret")))
	})

	It("returns an error for an unrecognised placeholder", func() {
		_, err := evaluateComputedValues(
			map[string]string{"app.val": "{{.Unknown}}"},
			"rel", "ns", "sec",
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("merges multiple paths into a single nested map", func() {
		result, err := evaluateComputedValues(
			map[string]string{
				"a.host": "{{.ReleaseName}}-db",
				"a.port": "5432",
			},
			"myrel", "myns", "",
		)
		Expect(err).NotTo(HaveOccurred())
		inner, ok := result["a"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(inner["host"]).To(Equal("myrel-db"))
		Expect(inner["port"]).To(Equal("5432"))
	})
})

var _ = Describe("dotNotationToNestedMap", func() {
	It("handles a simple dot-notation path", func() {
		result, err := dotNotationToNestedMap("a.b.c", "val")
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(HaveKeyWithValue("a",
			HaveKeyWithValue("b",
				HaveKeyWithValue("c", "val"))))
	})

	It("handles an array index in the middle of a path", func() {
		result, err := dotNotationToNestedMap("mlflow.extraVolumes[0].persistentVolumeClaim.claimName", "my-pvc")
		Expect(err).NotTo(HaveOccurred())
		mlflow, ok := result["mlflow"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		slice, ok := mlflow["extraVolumes"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(1))
		elem, ok := slice[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		pvc, ok := elem["persistentVolumeClaim"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(pvc).To(HaveKeyWithValue("claimName", "my-pvc"))
	})

	It("handles an array index as the last segment", func() {
		result, err := dotNotationToNestedMap("a.items[2]", "val")
		Expect(err).NotTo(HaveOccurred())
		a, ok := result["a"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		slice, ok := a["items"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(3))
		Expect(slice[2]).To(Equal("val"))
	})

	It("creates a slice of the right length for a non-zero index", func() {
		result, err := dotNotationToNestedMap("a.items[2].name", "val")
		Expect(err).NotTo(HaveOccurred())
		a, ok := result["a"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		slice, ok := a["items"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(3))
		Expect(slice[0]).To(BeNil())
		Expect(slice[1]).To(BeNil())
		elem, ok := slice[2].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(elem).To(HaveKeyWithValue("name", "val"))
	})

	It("handles an array index at the root level (no leading dot)", func() {
		result, err := dotNotationToNestedMap("items[0].name", "val")
		Expect(err).NotTo(HaveOccurred())
		slice, ok := result["items"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(1))
		elem, ok := slice[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(elem).To(HaveKeyWithValue("name", "val"))
	})

	It("returns an error for an array index exceeding maxArrayIndex", func() {
		_, err := dotNotationToNestedMap("a.items[1001].name", "val")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeds maximum"))
	})
})

var _ = Describe("buildAtPath", func() {
	It("merges two calls sharing an array key on the same map", func() {
		m := make(map[string]interface{})
		Expect(buildAtPath(m, []string{"extraVolumes[0]", "name"}, "artifacts")).To(Succeed())
		Expect(buildAtPath(m, []string{"extraVolumes[1]", "name"}, "config")).To(Succeed())
		slice, ok := m["extraVolumes"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(2))
		Expect(slice[0].(map[string]interface{})).To(HaveKeyWithValue("name", "artifacts"))
		Expect(slice[1].(map[string]interface{})).To(HaveKeyWithValue("name", "config"))
	})

	It("merges two calls sharing a map key on the same map", func() {
		m := make(map[string]interface{})
		Expect(buildAtPath(m, []string{"postgres", "host"}, "db-host")).To(Succeed())
		Expect(buildAtPath(m, []string{"postgres", "port"}, "5432")).To(Succeed())
		pg, ok := m["postgres"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(pg).To(HaveKeyWithValue("host", "db-host"))
		Expect(pg).To(HaveKeyWithValue("port", "5432"))
	})
})

var _ = Describe("mergeSlices", func() {
	It("merges two same-length slices of maps element-wise", func() {
		base := []interface{}{map[string]interface{}{"a": "1"}}
		override := []interface{}{map[string]interface{}{"b": "2"}}
		result := mergeSlices(base, override)
		Expect(result).To(HaveLen(1))
		elem := result[0].(map[string]interface{})
		Expect(elem).To(HaveKeyWithValue("a", "1"))
		Expect(elem).To(HaveKeyWithValue("b", "2"))
	})

	It("keeps base elements beyond override length", func() {
		base := []interface{}{map[string]interface{}{"a": "1"}, map[string]interface{}{"b": "2"}}
		override := []interface{}{map[string]interface{}{"x": "9"}}
		result := mergeSlices(base, override)
		Expect(result).To(HaveLen(2))
		Expect(result[0].(map[string]interface{})).To(HaveKeyWithValue("x", "9"))
		Expect(result[1].(map[string]interface{})).To(HaveKeyWithValue("b", "2"))
	})

	It("appends override elements beyond base length", func() {
		base := []interface{}{map[string]interface{}{"a": "1"}}
		override := []interface{}{map[string]interface{}{"x": "9"}, map[string]interface{}{"y": "8"}}
		result := mergeSlices(base, override)
		Expect(result).To(HaveLen(2))
		Expect(result[1].(map[string]interface{})).To(HaveKeyWithValue("y", "8"))
	})

	It("override wins for non-map scalar elements", func() {
		base := []interface{}{"old"}
		override := []interface{}{"new"}
		result := mergeSlices(base, override)
		Expect(result[0]).To(Equal("new"))
	})

	It("keeps base element when override element is nil", func() {
		base := []interface{}{map[string]interface{}{"a": "1"}, map[string]interface{}{"b": "2"}}
		override := []interface{}{nil, map[string]interface{}{"c": "3"}}
		result := mergeSlices(base, override)
		Expect(result[0].(map[string]interface{})).To(HaveKeyWithValue("a", "1"))
		Expect(result[1].(map[string]interface{})).To(HaveKeyWithValue("c", "3"))
	})
})

var _ = Describe("mergeMaps with slice-aware merging", func() {
	It("merges two computedValues targeting different indices of the same array", func() {
		first, _ := evaluateComputedValues(
			map[string]string{
				"mlflow.extraVolumes[0].persistentVolumeClaim.claimName": "{{.ReleaseName}}-artifacts",
			},
			"workspace156-mlflow", "workspace156", "",
		)
		second, _ := evaluateComputedValues(
			map[string]string{
				"mlflow.extraVolumes[1].name": "extra-vol",
			},
			"workspace156-mlflow", "workspace156", "",
		)
		merged := mergeMaps(first, second)
		mlflow := merged["mlflow"].(map[string]interface{})
		slice := mlflow["extraVolumes"].([]interface{})
		Expect(slice).To(HaveLen(2))
		elem0 := slice[0].(map[string]interface{})
		pvc := elem0["persistentVolumeClaim"].(map[string]interface{})
		Expect(pvc).To(HaveKeyWithValue("claimName", "workspace156-mlflow-artifacts"))
		elem1 := slice[1].(map[string]interface{})
		Expect(elem1).To(HaveKeyWithValue("name", "extra-vol"))
	})
})

var _ = Describe("evaluateComputedValues with array notation", func() {
	It("merges two paths targeting different indices of the same array in one call", func() {
		result, err := evaluateComputedValues(
			map[string]string{
				"mlflow.extraVolumes[0].persistentVolumeClaim.claimName": "{{.ReleaseName}}-artifacts",
				"mlflow.extraVolumes[1].name":                            "extra-vol",
			},
			"workspace156-mlflow", "workspace156", "",
		)
		Expect(err).NotTo(HaveOccurred())
		mlflow := result["mlflow"].(map[string]interface{})
		slice := mlflow["extraVolumes"].([]interface{})
		Expect(slice).To(HaveLen(2))
		elem0 := slice[0].(map[string]interface{})
		pvc := elem0["persistentVolumeClaim"].(map[string]interface{})
		Expect(pvc).To(HaveKeyWithValue("claimName", "workspace156-mlflow-artifacts"))
		elem1 := slice[1].(map[string]interface{})
		Expect(elem1).To(HaveKeyWithValue("name", "extra-vol"))
	})

	It("returns an error for an unrecognised placeholder inside an array path", func() {
		_, err := evaluateComputedValues(
			map[string]string{
				"mlflow.extraVolumes[0].persistentVolumeClaim.claimName": "{{.Unknown}}-artifacts",
			},
			"workspace156-mlflow", "workspace156", "",
		)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("resolves a release-name placeholder inside an array path", func() {
		result, err := evaluateComputedValues(
			map[string]string{
				"mlflow.extraVolumes[0].persistentVolumeClaim.claimName": "{{.ReleaseName}}-artifacts",
			},
			"workspace156-mlflow", "workspace156", "",
		)
		Expect(err).NotTo(HaveOccurred())
		mlflow, ok := result["mlflow"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		slice, ok := mlflow["extraVolumes"].([]interface{})
		Expect(ok).To(BeTrue())
		Expect(slice).To(HaveLen(1))
		elem, ok := slice[0].(map[string]interface{})
		Expect(ok).To(BeTrue())
		pvc, ok := elem["persistentVolumeClaim"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(pvc).To(HaveKeyWithValue("claimName", "workspace156-mlflow-artifacts"))
	})
})

var _ = Describe("autoValuesPrefix", func() {
	makeChart := func(depNames ...string) chart.Charter {
		deps := make([]*chartv2.Dependency, len(depNames))
		for i, name := range depNames {
			deps[i] = &chartv2.Dependency{Name: name}
		}
		return &chartv2.Chart{Metadata: &chartv2.Metadata{Dependencies: deps}}
	}

	It("returns the repo last segment for a single-dependency chart", func() {
		ch := makeChart("postgres")
		Expect(autoValuesPrefix(ch, "postgres")).To(Equal("postgres"))
	})

	It("returns empty string for an umbrella chart with multiple dependencies", func() {
		ch := makeChart("mlflow", "postgres")
		Expect(autoValuesPrefix(ch, "mlflow")).To(BeEmpty())
	})

	It("returns empty string for a chart with no dependencies", func() {
		ch := makeChart()
		Expect(autoValuesPrefix(ch, "standalone")).To(BeEmpty())
	})
})

var _ = Describe("helmReleaseStatus", func() {
	newFakeCfg := func() *action.Configuration {
		return &action.Configuration{
			Releases:     storage.Init(driver.NewMemory()),
			KubeClient:   &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}},
			Capabilities: chartcommon.DefaultCapabilities,
		}
	}

	It("returns not-found when release does not exist", func() {
		cfg := newFakeCfg()
		status, desc, err := helmReleaseStatus(cfg, "no-such-release")
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal("not-found"))
		Expect(desc).To(BeEmpty())
	})

	It("returns status and description for a deployed release", func() {
		cfg := newFakeCfg()
		rel := &releasev1.Release{
			Name:      "my-release",
			Namespace: "default",
			Version:   1,
			Info:      &releasev1.Info{Status: releasecommon.StatusDeployed, Description: "Install complete"},
			Chart:     &chartv2.Chart{Metadata: &chartv2.Metadata{Name: "test", Version: "1.0.0"}},
		}
		Expect(cfg.Releases.Create(rel)).To(Succeed())

		status, desc, err := helmReleaseStatus(cfg, "my-release")
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal("deployed"))
		Expect(desc).To(Equal("Install complete"))
	})

	It("returns empty description when Info.Description is not set", func() {
		cfg := newFakeCfg()
		rel := &releasev1.Release{
			Name:      "no-desc-release",
			Namespace: "default",
			Version:   1,
			Info:      &releasev1.Info{Status: releasecommon.StatusPendingInstall},
			Chart:     &chartv2.Chart{Metadata: &chartv2.Metadata{Name: "test", Version: "1.0.0"}},
		}
		Expect(cfg.Releases.Create(rel)).To(Succeed())

		status, desc, err := helmReleaseStatus(cfg, "no-desc-release")
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal("pending-install"))
		Expect(desc).To(BeEmpty())
	})
})

var _ = Describe("helmInstallOrUpgrade", func() {
	newFakeCfg := func() *action.Configuration {
		return &action.Configuration{
			Releases:     storage.Init(driver.NewMemory()),
			KubeClient:   &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}},
			Capabilities: chartcommon.DefaultCapabilities,
		}
	}

	It("returns an error when the chart has no version in metadata", func() {
		cfg := newFakeCfg()
		ch := &chartv2.Chart{Metadata: &chartv2.Metadata{Name: "test"}} // no Version field
		err := helmInstallOrUpgrade(context.Background(), cfg, "default", "my-release", ch, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("no version in metadata"))
	})

	It("does not return a metadata error when the chart has a version", func() {
		cfg := newFakeCfg()
		ch := &chartv2.Chart{Metadata: &chartv2.Metadata{Name: "test", Version: "1.0.0"}}
		err := helmInstallOrUpgrade(context.Background(), cfg, "default", "versioned-release", ch, nil)
		// With a correct version field the install must pass the version check.
		// The fake kube client returns no error, so the whole call succeeds.
		Expect(err).NotTo(HaveOccurred())
	})
})

func newChartWithChorusFile(content string) *chartv2.Chart {
	files := []*chartcommon.File{}
	if content != "" {
		files = append(files, &chartcommon.File{Name: "chorus.yaml", Data: []byte(content)})
	}
	return &chartv2.Chart{
		Metadata: &chartv2.Metadata{Name: "test", Version: "1.0.0"},
		Files:    files,
	}
}

var _ = Describe("parseChorusConfig", func() {
	It("returns nil when the chart has no chorus.yaml", func() {
		ch := newChartWithChorusFile("")
		cfg, err := parseChorusConfig(ch)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).To(BeNil())
	})

	It("parses a well-formed chorus.yaml", func() {
		content := `
values:
  foo:
    bar: "{{.ReleaseName}}-x"
credentials:
  secretName: "{{.ReleaseName}}-creds"
  paths:
    - foo.password
connectionInfoTemplate: "https://{{.ReleaseName}}.{{.Namespace}}"
`
		cfg, err := parseChorusConfig(newChartWithChorusFile(content))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.ConnectionInfoTemplate).To(Equal("https://{{.ReleaseName}}.{{.Namespace}}"))
		Expect(cfg.Credentials).NotTo(BeNil())
		Expect(cfg.Credentials.SecretName).To(Equal("{{.ReleaseName}}-creds"))
		Expect(cfg.Credentials.Paths).To(Equal([]string{"foo.password"}))
		Expect(cfg.Values).To(HaveKey("foo"))
	})

	It("rejects malformed YAML", func() {
		_, err := parseChorusConfig(newChartWithChorusFile("values: [not-a-map"))
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("parsing chorus.yaml"))
	})

	It("rejects unknown top-level keys (strict)", func() {
		_, err := parseChorusConfig(newChartWithChorusFile("nope: 1\n"))
		Expect(err).To(HaveOccurred())
	})

	It("parses a chorus.yaml that contains only empty blocks", func() {
		cfg, err := parseChorusConfig(newChartWithChorusFile("values: {}\ncredentials: {}\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Values).To(BeEmpty())
		Expect(cfg.Credentials).NotTo(BeNil())
		Expect(cfg.Credentials.Paths).To(BeEmpty())
		Expect(cfg.Credentials.SecretName).To(BeEmpty())
	})

	It("parses a chorus.yaml that contains only comments", func() {
		cfg, err := parseChorusConfig(newChartWithChorusFile("# just a comment\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Values).To(BeNil())
		Expect(cfg.Credentials).To(BeNil())
		Expect(cfg.ConnectionInfoTemplate).To(BeEmpty())
	})

	It("finds chorus.yaml when it is not the first entry in chart.Files", func() {
		ch := &chartv2.Chart{
			Metadata: &chartv2.Metadata{Name: "test", Version: "1.0.0"},
			Files: []*chartcommon.File{
				{Name: "decoy-before.yaml", Data: []byte("garbage: [")},
				{Name: "chorus.yaml", Data: []byte("connectionInfoTemplate: hello\n")},
				{Name: "decoy-after.yaml", Data: []byte("more: garbage")},
			},
		}
		cfg, err := parseChorusConfig(ch)
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.ConnectionInfoTemplate).To(Equal("hello"))
	})
})

var _ = Describe("effectiveSecretName", func() {
	It("returns the CR field when set", func() {
		svc := &defaultv1alpha1.WorkspaceService{
			Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{SecretName: "explicit-name"},
		}
		got, err := effectiveSecretName(svc, nil, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("explicit-name"))
	})

	It("substitutes placeholders in the CR field", func() {
		svc := &defaultv1alpha1.WorkspaceService{
			Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{SecretName: "{{.ReleaseName}}-creds"},
		}
		got, err := effectiveSecretName(svc, nil, "my-release", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("my-release-creds"))
	})

	It("falls through to chorus.yaml when CR is empty and substitutes placeholders", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{SecretName: "{{.ReleaseName}}-creds"}}
		got, err := effectiveSecretName(svc, cfg, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-creds"))
	})

	It("defaults to <release>-creds when both are empty", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		got, err := effectiveSecretName(svc, nil, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-creds"))
	})

	It("falls through to default when CR has Credentials struct with empty SecretName and chorus.yaml is nil", func() {
		// Different branch from "Credentials = nil": exercises the empty-string check inside the if.
		svc := &defaultv1alpha1.WorkspaceService{Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{Paths: []string{"pw"}}}
		got, err := effectiveSecretName(svc, nil, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-creds"))
	})

	It("returns an error when the CR field has an unrecognised placeholder", func() {
		svc := &defaultv1alpha1.WorkspaceService{
			Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{SecretName: "{{.Bogus}}-creds"},
		}
		_, err := effectiveSecretName(svc, nil, "rel", "ns")
		Expect(err).To(HaveOccurred())
	})

	It("returns an error when the chorus.yaml field has an unrecognised placeholder", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{SecretName: "{{.Bogus}}-creds"}}
		_, err := effectiveSecretName(svc, cfg, "rel", "ns")
		Expect(err).To(HaveOccurred())
	})

	It("falls through to default when chorus.yaml has no credentials block", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		cfg := &ChorusConfig{} // no Credentials field
		got, err := effectiveSecretName(svc, cfg, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-creds"))
	})

	It("falls through to default when both CR and chorus.yaml have empty SecretName", func() {
		svc := &defaultv1alpha1.WorkspaceService{Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{}}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{Paths: []string{"pw"}}}
		got, err := effectiveSecretName(svc, cfg, "rel", "ns")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-creds"))
	})
})

var _ = Describe("resolvePlaceholders", func() {
	It("substitutes the three supported placeholders", func() {
		got, err := resolvePlaceholders("ns={{.Namespace}} rel={{.ReleaseName}} sec={{.SecretName}}", "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("ns=ns rel=rel sec=sec"))
	})

	It("returns the input unchanged when no placeholders are present", func() {
		got, err := resolvePlaceholders("plain-string", "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("plain-string"))
	})

	It("returns an error when an unrecognised placeholder remains", func() {
		_, err := resolvePlaceholders("hello {{.Unknown}}", "rel", "ns", "sec")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("returns the empty string for empty input", func() {
		got, err := resolvePlaceholders("", "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("substitutes the same placeholder multiple times", func() {
		got, err := resolvePlaceholders("{{.ReleaseName}}-{{.ReleaseName}}", "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal("rel-rel"))
	})
})

var _ = Describe("effectiveCredentialPaths", func() {
	It("returns CR paths when non-empty", func() {
		svc := &defaultv1alpha1.WorkspaceService{
			Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{Paths: []string{"a", "b"}},
		}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{Paths: []string{"x"}}}
		Expect(effectiveCredentialPaths(svc, cfg)).To(Equal([]string{"a", "b"}))
	})

	It("falls through to chorus.yaml when CR paths are empty", func() {
		svc := &defaultv1alpha1.WorkspaceService{Credentials: &defaultv1alpha1.WorkspaceServiceCredentials{}}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{Paths: []string{"x"}}}
		Expect(effectiveCredentialPaths(svc, cfg)).To(Equal([]string{"x"}))
	})

	It("falls through to chorus.yaml when CR has no Credentials block at all", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		cfg := &ChorusConfig{Credentials: &ChorusConfigCredentials{Paths: []string{"x"}}}
		Expect(effectiveCredentialPaths(svc, cfg)).To(Equal([]string{"x"}))
	})

	It("returns nil when both are empty", func() {
		Expect(effectiveCredentialPaths(&defaultv1alpha1.WorkspaceService{}, nil)).To(BeNil())
	})

	It("returns nil when chorus.yaml has no credentials block", func() {
		Expect(effectiveCredentialPaths(&defaultv1alpha1.WorkspaceService{}, &ChorusConfig{})).To(BeNil())
	})
})

var _ = Describe("effectiveConnectionInfoTemplate", func() {
	It("returns the CR field when set", func() {
		svc := &defaultv1alpha1.WorkspaceService{ConnectionInfoTemplate: "from-cr"}
		cfg := &ChorusConfig{ConnectionInfoTemplate: "from-chorus"}
		Expect(effectiveConnectionInfoTemplate(svc, cfg)).To(Equal("from-cr"))
	})

	It("falls through to chorus.yaml when CR is empty", func() {
		svc := &defaultv1alpha1.WorkspaceService{}
		cfg := &ChorusConfig{ConnectionInfoTemplate: "from-chorus"}
		Expect(effectiveConnectionInfoTemplate(svc, cfg)).To(Equal("from-chorus"))
	})

	It("returns empty string when both are empty", func() {
		Expect(effectiveConnectionInfoTemplate(&defaultv1alpha1.WorkspaceService{}, nil)).To(BeEmpty())
	})

	It("returns empty string when chorus.yaml is non-nil but has empty template", func() {
		Expect(effectiveConnectionInfoTemplate(&defaultv1alpha1.WorkspaceService{}, &ChorusConfig{})).To(BeEmpty())
	})
})

var _ = Describe("substituteChorusPlaceholders", func() {
	It("substitutes placeholders in nested maps and slices", func() {
		values := map[string]interface{}{
			"top": "{{.ReleaseName}}-x",
			"nested": map[string]interface{}{
				"inner": "ns={{.Namespace}}",
			},
			"list": []interface{}{"sec={{.SecretName}}", 42},
		}
		out, err := substituteChorusPlaceholders(values, "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(out["top"]).To(Equal("rel-x"))
		Expect(out["nested"].(map[string]interface{})["inner"]).To(Equal("ns=ns"))
		Expect(out["list"].([]interface{})[0]).To(Equal("sec=sec"))
		Expect(out["list"].([]interface{})[1]).To(Equal(42))
	})

	It("returns nil when input is empty", func() {
		out, err := substituteChorusPlaceholders(nil, "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(out).To(BeNil())
	})

	It("returns an error when an unrecognised placeholder remains", func() {
		_, err := substituteChorusPlaceholders(map[string]interface{}{"k": "{{.Bogus}}"}, "rel", "ns", "sec")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("propagates the unknown-placeholder error from a nested map", func() {
		values := map[string]interface{}{
			"outer": map[string]interface{}{"inner": "{{.Bogus}}"},
		}
		_, err := substituteChorusPlaceholders(values, "rel", "ns", "sec")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("propagates the unknown-placeholder error from inside a slice", func() {
		values := map[string]interface{}{
			"list": []interface{}{"ok", "{{.Bogus}}"},
		}
		_, err := substituteChorusPlaceholders(values, "rel", "ns", "sec")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("unrecognised placeholder"))
	})

	It("leaves non-string scalars and nil values untouched (default case)", func() {
		values := map[string]interface{}{
			"int":   42,
			"bool":  true,
			"float": 3.14,
			"nil":   nil,
		}
		out, err := substituteChorusPlaceholders(values, "rel", "ns", "sec")
		Expect(err).NotTo(HaveOccurred())
		Expect(out["int"]).To(Equal(42))
		Expect(out["bool"]).To(Equal(true))
		Expect(out["float"]).To(Equal(3.14))
		Expect(out["nil"]).To(BeNil())
	})
})
