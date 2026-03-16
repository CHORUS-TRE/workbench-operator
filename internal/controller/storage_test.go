package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// newTestReconciler builds a WorkbenchReconciler wired to the testenv client.
func newTestReconciler(cfg Config) *WorkbenchReconciler {
	return &WorkbenchReconciler{
		Client:   k8sClient,
		Scheme:   k8sClient.Scheme(),
		Recorder: record.NewFakeRecorder(10),
		Config:   cfg,
	}
}

var _ = Describe("StorageManager", func() {
	ctx := context.Background()

	Describe("GetProvider", func() {
		It("returns the S3 provider", func() {
			sm := NewStorageManager(newTestReconciler(Config{}))
			p := sm.GetProvider(StorageTypeS3)
			Expect(p).NotTo(BeNil())
			Expect(p.GetStorageType()).To(Equal(StorageTypeS3))
		})

		It("returns the NFS provider", func() {
			sm := NewStorageManager(newTestReconciler(Config{}))
			p := sm.GetProvider(StorageTypeNFS)
			Expect(p).NotTo(BeNil())
			Expect(p.GetStorageType()).To(Equal(StorageTypeNFS))
		})

		It("includes local provider when LocalStorageEnabled is true", func() {
			sm := NewStorageManager(newTestReconciler(Config{LocalStorageEnabled: true}))
			p := sm.GetProvider(StorageTypeLocal)
			Expect(p).NotTo(BeNil())
			Expect(p.GetStorageType()).To(Equal(StorageTypeLocal))
		})

		It("returns nil for local provider when not enabled", func() {
			sm := NewStorageManager(newTestReconciler(Config{LocalStorageEnabled: false}))
			Expect(sm.GetProvider(StorageTypeLocal)).To(BeNil())
		})
	})

	Describe("BaseProvider getters (via S3Provider)", func() {
		var p *S3Provider

		BeforeEach(func() {
			p = NewS3Provider(newTestReconciler(Config{
				JuiceFSSecretName:      "my-juicefs-secret",
				JuiceFSSecretNamespace: "my-namespace",
			}))
		})

		It("GetSecretName returns the secret name", func() {
			Expect(p.GetSecretName()).To(Equal("my-juicefs-secret"))
		})

		It("GetSecretNamespace returns the secret namespace", func() {
			Expect(p.GetSecretNamespace()).To(Equal("my-namespace"))
		})

		It("GetMountType returns the mount type", func() {
			Expect(p.GetMountType()).NotTo(BeEmpty())
		})

		It("GetVolumeName includes mount type", func() {
			Expect(p.GetVolumeName()).To(ContainSubstring(p.GetMountType()))
		})

		It("GetPVCName includes namespace and mount type", func() {
			Expect(p.GetPVCName("mynamespace")).To(ContainSubstring("mynamespace"))
			Expect(p.GetPVCName("mynamespace")).To(ContainSubstring(p.GetMountType()))
		})

		It("GetVolumeSpec returns a PVC-backed volume", func() {
			vol := p.GetVolumeSpec("my-pvc")
			Expect(vol.PersistentVolumeClaim).NotTo(BeNil())
			Expect(vol.PersistentVolumeClaim.ClaimName).To(Equal("my-pvc"))
		})

		It("GetVolumeMountSpecs returns data mount", func() {
			mounts := p.GetVolumeMountSpecs("alice", "ns1")
			Expect(mounts).NotTo(BeEmpty())
			var paths []string
			for _, m := range mounts {
				paths = append(paths, m.MountPath)
			}
			Expect(paths).To(ContainElement(ContainSubstring(p.GetMountType())))
		})

		It("CreatePV (BaseProvider) returns a PersistentVolume struct without k8s call", func() {
			pv, err := p.BaseProvider.CreatePV("ns1", map[string]string{"key": "val"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(pv).NotTo(BeNil())
			Expect(pv.Spec.CSI).NotTo(BeNil())
		})

		It("CreatePVC returns a PersistentVolumeClaim struct without k8s call", func() {
			wb := defaultv1alpha1.Workbench{
				ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "ns1"},
			}
			pvc, err := p.BaseProvider.CreatePVC(ctx, wb)
			Expect(err).NotTo(HaveOccurred())
			Expect(pvc).NotTo(BeNil())
			Expect(pvc.Namespace).To(Equal("ns1"))
		})

		It("HasSecret returns false when secret does not exist", func() {
			Expect(p.HasSecret(ctx, k8sClient)).To(BeFalse())
		})
	})

	Describe("BaseProvider getters (via S3Provider — mountAppData path)", func() {
		It("GetVolumeMountSpecs includes app_data mount for S3", func() {
			p := NewS3Provider(newTestReconciler(Config{}))
			mounts := p.GetVolumeMountSpecs("alice", "ns1")
			var paths []string
			for _, m := range mounts {
				paths = append(paths, m.MountPath)
			}
			Expect(paths).To(ContainElement("/mnt/app_data"))
		})

		It("GetVolumeMountSpecs does NOT include app_data mount for NFS", func() {
			p := NewNFSProvider(newTestReconciler(Config{}))
			mounts := p.GetVolumeMountSpecs("alice", "ns1")
			var paths []string
			for _, m := range mounts {
				paths = append(paths, m.MountPath)
			}
			Expect(paths).NotTo(ContainElement("/mnt/app_data"))
		})
	})

	Describe("HasSecret with real secret", func() {
		const secretName = "storage-test-secret"
		const secretNS = "default"

		AfterEach(func() {
			s := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNS}, s); err == nil {
				_ = k8sClient.Delete(ctx, s)
			}
		})

		It("HasSecret returns true when secret exists with required 'name' field", func() {
			// S3Provider.HasSecret calls getSecretConfig which checks secret.Data["name"]
			p := NewS3Provider(newTestReconciler(Config{
				JuiceFSSecretName:      secretName,
				JuiceFSSecretNamespace: secretNS,
			}))
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
				Data:       map[string][]byte{"name": []byte("myjuicefs")},
			})).To(Succeed())

			Expect(p.HasSecret(ctx, k8sClient)).To(BeTrue())
		})
	})
})

var _ = Describe("LocalProvider", func() {
	ctx := context.Background()

	It("HasDriver returns true when LocalStorageEnabled", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: true}))
		Expect(p.HasDriver(ctx, k8sClient)).To(BeTrue())
	})

	It("HasDriver returns false when LocalStorageEnabled is false", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: false}))
		Expect(p.HasDriver(ctx, k8sClient)).To(BeFalse())
	})

	It("HasSecret returns true when LocalStorageEnabled", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: true}))
		Expect(p.HasSecret(ctx, k8sClient)).To(BeTrue())
	})

	It("HasSecret returns false when LocalStorageEnabled is false", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: false}))
		Expect(p.HasSecret(ctx, k8sClient)).To(BeFalse())
	})

	It("CreatePV returns a hostPath-backed PV struct", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: true, LocalStorageHostPath: "/data"}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns"},
		}
		pv, err := p.CreatePV(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(pv).NotTo(BeNil())
		Expect(pv.Spec.HostPath).NotTo(BeNil())
		Expect(pv.Spec.HostPath.Path).To(Equal("/data"))
	})

	It("CreatePV uses default host path when not configured", func() {
		p := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: true}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb2", Namespace: "testns2"},
		}
		pv, err := p.CreatePV(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(pv.Spec.HostPath.Path).To(Equal("/tmp/workbench-local-storage"))
	})
})

var _ = Describe("S3Provider.CreatePV", func() {
	ctx := context.Background()
	const secretName = "s3-createpv-secret"
	const secretNS = "default"

	AfterEach(func() {
		s := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNS}, s); err == nil {
			_ = k8sClient.Delete(ctx, s)
		}
	})

	It("returns a CSI PV struct when secret has 'name' field", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data:       map[string][]byte{"name": []byte("myjuicefs")},
		})).To(Succeed())

		p := NewS3Provider(newTestReconciler(Config{
			JuiceFSSecretName:      secretName,
			JuiceFSSecretNamespace: secretNS,
		}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns-s3"},
		}
		pv, err := p.CreatePV(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(pv).NotTo(BeNil())
		Expect(pv.Spec.CSI).NotTo(BeNil())
		Expect(pv.Spec.CSI.Driver).To(Equal("csi.juicefs.com"))
		Expect(pv.Spec.CSI.VolumeAttributes["name"]).To(Equal("myjuicefs"))
	})

	It("returns an error when secret has no 'name' field", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data:       map[string][]byte{},
		})).To(Succeed())

		p := NewS3Provider(newTestReconciler(Config{
			JuiceFSSecretName:      secretName,
			JuiceFSSecretNamespace: secretNS,
		}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns-s3"},
		}
		_, err := p.CreatePV(ctx, wb)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("missing required 'name' field"))
	})
})

var _ = Describe("NFSProvider", func() {
	ctx := context.Background()
	const secretName = "nfs-createpv-secret"
	const secretNS = "default"

	AfterEach(func() {
		s := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNS}, s); err == nil {
			_ = k8sClient.Delete(ctx, s)
		}
	})

	It("HasSecret returns true when secret has 'server' and 'share' fields", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data: map[string][]byte{
				"server": []byte("nfs.example.com"),
				"share":  []byte("/exports/data"),
			},
		})).To(Succeed())

		p := NewNFSProvider(newTestReconciler(Config{
			NFSSecretName:      secretName,
			NFSSecretNamespace: secretNS,
		}))
		Expect(p.HasSecret(ctx, k8sClient)).To(BeTrue())
	})

	It("HasSecret returns false when secret has no 'server' field", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data:       map[string][]byte{"share": []byte("/exports/data")},
		})).To(Succeed())

		p := NewNFSProvider(newTestReconciler(Config{
			NFSSecretName:      secretName,
			NFSSecretNamespace: secretNS,
		}))
		Expect(p.HasSecret(ctx, k8sClient)).To(BeFalse())
	})

	It("HasSecret returns false when secret has no 'share' field", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data:       map[string][]byte{"server": []byte("nfs.example.com")},
		})).To(Succeed())

		p := NewNFSProvider(newTestReconciler(Config{
			NFSSecretName:      secretName,
			NFSSecretNamespace: secretNS,
		}))
		Expect(p.HasSecret(ctx, k8sClient)).To(BeFalse())
	})

	It("CreatePV returns a CSI PV struct when secret is valid", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data: map[string][]byte{
				"server": []byte("nfs.example.com"),
				"share":  []byte("/exports/data"),
			},
		})).To(Succeed())

		p := NewNFSProvider(newTestReconciler(Config{
			NFSSecretName:      secretName,
			NFSSecretNamespace: secretNS,
		}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns-nfs"},
		}
		pv, err := p.CreatePV(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(pv).NotTo(BeNil())
		Expect(pv.Spec.CSI).NotTo(BeNil())
		Expect(pv.Spec.CSI.Driver).To(Equal("nfs.csi.k8s.io"))
		Expect(pv.Spec.CSI.VolumeAttributes["server"]).To(Equal("nfs.example.com"))
	})
})

var _ = Describe("StorageManager.GetEnabledProviders", func() {
	It("returns S3 and NFS by default when spec storage is nil", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb", Namespace: "ns"},
		}
		providers := sm.GetEnabledProviders(wb)
		types := make([]StorageType, 0)
		for _, p := range providers {
			types = append(types, p.GetStorageType())
		}
		Expect(types).To(ContainElements(StorageTypeS3, StorageTypeNFS))
	})

	It("includes local provider when spec and config both enable it", func() {
		sm := NewStorageManager(newTestReconciler(Config{LocalStorageEnabled: true}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb", Namespace: "ns"},
			Spec:       defaultv1alpha1.WorkbenchSpec{Storage: &defaultv1alpha1.StorageConfig{Local: true}},
		}
		providers := sm.GetEnabledProviders(wb)
		types := make([]StorageType, 0)
		for _, p := range providers {
			types = append(types, p.GetStorageType())
		}
		Expect(types).To(ContainElement(StorageTypeLocal))
	})

	It("returns only S3 when only S3 is enabled in spec", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb", Namespace: "ns"},
			Spec:       defaultv1alpha1.WorkbenchSpec{Storage: &defaultv1alpha1.StorageConfig{S3: true, NFS: false}},
		}
		providers := sm.GetEnabledProviders(wb)
		Expect(providers).To(HaveLen(1))
		Expect(providers[0].GetStorageType()).To(Equal(StorageTypeS3))
	})

	It("returns empty slice when all storage types are explicitly disabled", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb", Namespace: "ns"},
			Spec:       defaultv1alpha1.WorkbenchSpec{Storage: &defaultv1alpha1.StorageConfig{S3: false, NFS: false, Local: false}},
		}
		Expect(sm.GetEnabledProviders(wb)).To(BeEmpty())
	})
})

var _ = Describe("StorageManager.GetVolumeAndMountSpecs", func() {
	ctx := context.Background()
	const secretName = "s3-volspecs-secret"
	const secretNS = "default"

	AfterEach(func() {
		s := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: secretNS}, s); err == nil {
			_ = k8sClient.Delete(ctx, s)
		}
	})

	It("returns empty volumes and mounts when no providers have valid secrets", func() {
		sm := NewStorageManager(newTestReconciler(Config{
			JuiceFSSecretName:      "nonexistent-secret",
			JuiceFSSecretNamespace: secretNS,
		}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns"},
			Spec: defaultv1alpha1.WorkbenchSpec{
				Storage: &defaultv1alpha1.StorageConfig{S3: true, NFS: false},
			},
		}
		vols, mounts, err := sm.GetVolumeAndMountSpecs(ctx, wb, "alice", "testns")
		Expect(err).NotTo(HaveOccurred())
		Expect(vols).To(BeEmpty())
		Expect(mounts).To(BeEmpty())
	})

	It("returns volumes and mounts when S3 provider has valid secret", func() {
		Expect(k8sClient.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: secretNS},
			Data:       map[string][]byte{"name": []byte("myjuicefs")},
		})).To(Succeed())

		sm := NewStorageManager(newTestReconciler(Config{
			JuiceFSSecretName:      secretName,
			JuiceFSSecretNamespace: secretNS,
		}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns-specs"},
			Spec: defaultv1alpha1.WorkbenchSpec{
				Storage: &defaultv1alpha1.StorageConfig{S3: true, NFS: false},
			},
		}
		vols, mounts, err := sm.GetVolumeAndMountSpecs(ctx, wb, "alice", "testns-specs")
		Expect(err).NotTo(HaveOccurred())
		Expect(vols).NotTo(BeEmpty())
		Expect(mounts).NotTo(BeEmpty())
	})

	It("returns no volumes when storage config is nil and no secrets available", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb1", Namespace: "testns"},
		}
		vols, mounts, err := sm.GetVolumeAndMountSpecs(ctx, wb, "alice", "testns")
		Expect(err).NotTo(HaveOccurred())
		Expect(vols).To(BeEmpty())
		Expect(mounts).To(BeEmpty())
	})
})

var _ = Describe("StorageManager.ProcessEnabledStorage", func() {
	ctx := context.Background()

	It("returns an empty map when no storage is enabled", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb-proc-none", Namespace: "default"},
			Spec:       defaultv1alpha1.WorkbenchSpec{Storage: &defaultv1alpha1.StorageConfig{S3: false, NFS: false}},
		}
		result, err := sm.ProcessEnabledStorage(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
	})

	It("returns an empty map and no error when drivers are unavailable", func() {
		// S3 and NFS require CSI drivers not present in testenv — both skip gracefully
		sm := NewStorageManager(newTestReconciler(Config{}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb-proc-nodrivers", Namespace: "default"},
			Spec:       defaultv1alpha1.WorkbenchSpec{Storage: &defaultv1alpha1.StorageConfig{S3: true, NFS: true}},
		}
		result, err := sm.ProcessEnabledStorage(ctx, wb)
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(BeEmpty())
	})
})

var _ = Describe("StorageManager.processStorageProvider", func() {
	ctx := context.Background()

	It("returns empty string when driver is not available", func() {
		sm := NewStorageManager(newTestReconciler(Config{}))
		// LocalProvider with LocalStorageEnabled=false → HasDriver returns false
		provider := NewLocalProvider(newTestReconciler(Config{LocalStorageEnabled: false}))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb-proc-provider", Namespace: "default"},
		}
		pvcName, err := sm.processStorageProvider(ctx, wb, provider)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcName).To(BeEmpty())
	})

	It("returns empty string when driver is present but secret is missing", func() {
		// Create the JuiceFS CSIDriver so HasDriver returns true
		csiDriver := &storagev1.CSIDriver{
			ObjectMeta: metav1.ObjectMeta{Name: "csi.juicefs.com"},
		}
		_ = k8sClient.Create(ctx, csiDriver)

		defer func() {
			_ = k8sClient.Delete(ctx, csiDriver)
		}()

		cfg := Config{
			JuiceFSSecretName:      "nonexistent-juicefs-secret-xyz",
			JuiceFSSecretNamespace: "default",
		}
		sm := NewStorageManager(newTestReconciler(cfg))
		provider := NewS3Provider(newTestReconciler(cfg))
		wb := defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{Name: "wb-proc-nosecret", Namespace: "default"},
		}
		pvcName, err := sm.processStorageProvider(ctx, wb, provider)
		Expect(err).NotTo(HaveOccurred())
		Expect(pvcName).To(BeEmpty())
	})
})

var _ = Describe("generatePassword", func() {
	It("returns a string of the requested length", func() {
		pw, err := generatePassword(24)
		Expect(err).NotTo(HaveOccurred())
		Expect(pw).To(HaveLen(24))
	})

	It("returns different passwords on successive calls", func() {
		pw1, _ := generatePassword(24)
		pw2, _ := generatePassword(24)
		Expect(pw1).NotTo(Equal(pw2))
	})

	It("works for different lengths", func() {
		for _, length := range []int{8, 16, 32, 64} {
			pw, err := generatePassword(length)
			Expect(err).NotTo(HaveOccurred())
			Expect(pw).To(HaveLen(length))
		}
	})
})
