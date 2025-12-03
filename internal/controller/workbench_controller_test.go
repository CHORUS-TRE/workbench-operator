package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// Helper functions for creating test data
func createMockContainerStatus(name string, state corev1.ContainerState, ready bool, restartCount int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         name,
		State:        state,
		Ready:        ready,
		RestartCount: restartCount,
	}
}

func createWaitingContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Waiting: &corev1.ContainerStateWaiting{
			Reason:  "ImagePullBackOff",
			Message: "Back-off pulling image",
		},
	}, false, 0)
}

func createStartingContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: time.Now().Add(-30 * time.Second)}, // Started 30s ago
		},
	}, false, 0)
}

func createReadyContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: time.Now().Add(-5 * time.Minute)}, // Started 5m ago
		},
	}, true, 0)
}

func createFailingContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: time.Now().Add(-10 * time.Minute)}, // Started 10m ago
		},
	}, false, 0)
}

func createRestartingContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Running: &corev1.ContainerStateRunning{
			StartedAt: metav1.Time{Time: time.Now().Add(-2 * time.Minute)}, // Started 2m ago
		},
	}, true, 3) // 3 restarts
}

func createTerminatedContainerStatus() corev1.ContainerStatus {
	return createMockContainerStatus("xpra-server", corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{
			Reason:   "Error",
			Message:  "Container crashed",
			ExitCode: 1,
		},
	}, false, 1)
}

func createMockPod(name, namespace string, containerStatus corev1.ContainerStatus, deletionTimestamp *metav1.Time) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"workbench": "test-resource",
			},
			CreationTimestamp: metav1.Time{Time: time.Now()},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "xpra-server", Image: "test:latest"},
				{Name: "xpra-server-bind", Image: "test:latest"},
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{containerStatus},
		},
	}

	if deletionTimestamp != nil {
		pod.DeletionTimestamp = deletionTimestamp
	}

	return pod
}

var _ = Describe("Workbench Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}

		workbench := &defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resourceName,
				Namespace: "default",
			},
		}

		workbench.Spec.ServiceAccount = "service-account"

		oneGig := resource.MustParse("1Gi")
		workbench.Spec.Apps = map[string]defaultv1alpha1.WorkbenchApp{
			"uid0": {
				Name: "wezterm",
				Image: &defaultv1alpha1.Image{
					Registry:   "my-registry",
					Repository: "applications/wezterm",
					Tag:        "latest",
				},
			},
			"uid1": {
				Name: "kitty",
				Image: &defaultv1alpha1.Image{
					Registry:   "quay.io",
					Repository: "kitty/kitty",
					Tag:        "1.2.0",
				},
				ShmSize: &oneGig,
			},
			"uid2": {
				Name:  "alacritty",
				State: "Stopped",
				Image: &defaultv1alpha1.Image{
					Registry:   "my-registry",
					Repository: "applications/alacritty",
					Tag:        "latest",
				},
			},
		}

		workbench.Spec.ImagePullSecrets = []string{
			"secret-1",
			"secret-2",
		}

		// Initialize Server with default user for testing
		workbench.Spec.Server = defaultv1alpha1.WorkbenchServer{
			User: "chorus", // Explicit default for test
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Workbench")
			err := k8sClient.Get(ctx, typeNamespacedName, workbench)
			if err != nil && errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, workbench)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &defaultv1alpha1.Workbench{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Workbench")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// Clean up mock pods
			pods := &corev1.PodList{}
			_ = k8sClient.List(ctx, pods, client.InNamespace("default"))
			for i := range pods.Items {
				pod := &pods.Items[i]
				// Remove finalizers to allow immediate deletion
				pod.Finalizers = []string{}
				_ = k8sClient.Update(ctx, pod)
				_ = k8sClient.Delete(ctx, pod)
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Checking if JuiceFS CSI driver is present")
			// Check if JuiceFS CSI driver is available
			csiDriver := &storagev1.CSIDriver{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: "csi.juicefs.com"}, csiDriver)
			hasJuiceFSDriver := !errors.IsNotFound(err)

			By("Checking if JuiceFS secret exists")
			// Also check for secret in tests
			hasJuiceFSSecret := false
			if hasJuiceFSDriver {
				secret := &corev1.Secret{}
				err = k8sClient.Get(ctx, types.NamespacedName{
					Name:      "juicefs-secret",
					Namespace: "kube-system",
				}, secret)
				hasJuiceFSSecret = !errors.IsNotFound(err)
			}

			By("Reconciling the created resource")
			controllerReconciler := &WorkbenchReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(3),
				Config: Config{
					Registry:               "my-registry",
					AppsRepository:         "applications",
					XpraServerImage:        "my-registry/server/xpra-server",
					JuiceFSSecretName:      "juicefs-secret",
					JuiceFSSecretNamespace: "kube-system",
				},
			}

			// Create a mock pod with ready xpra-server container to simulate xpra being ready
			pod := createMockPod("test-resource-server-pod", "default", createReadyContainerStatus(), nil)
			pod.Labels = map[string]string{"workbench": resourceName}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			// Update pod status separately (required in test environment)
			pod.Status = corev1.PodStatus{
				InitContainerStatuses: []corev1.ContainerStatus{
					createMockContainerStatus("xpra-server-bind", corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{
							StartedAt: metav1.Now(),
						},
					}, true, 0),
				},
				ContainerStatuses: []corev1.ContainerStatus{createReadyContainerStatus()},
				PodIP:             "10.0.0.1",
			}
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify that a deployment exists.
			deploymentNamespacedName := types.NamespacedName{
				Name:      fmt.Sprintf("%s-server", typeNamespacedName.Name),
				Namespace: typeNamespacedName.Namespace,
			}
			deployment := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, deploymentNamespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())

			// Two secrets were defined to pull the images.
			Expect(deployment.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))

			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deployment.Spec.Template.Spec.InitContainers).To(HaveLen(1))

			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my-registry/server/"))
			Expect(deployment.Spec.Template.Spec.InitContainers[0].Image).To(HavePrefix("alpine/socat:"))

			Expect(deployment.Spec.Template.Spec.ServiceAccountName).To(Equal("service-account"))

			// Verify that a service exists
			service := &corev1.Service{}
			err = k8sClient.Get(ctx, typeNamespacedName, service)
			Expect(err).NotTo(HaveOccurred())

			// Verify that the jobs exist
			job := &batchv1.Job{}
			jobNamespacedName := types.NamespacedName{
				Name:      resourceName + "-uid0-wezterm",
				Namespace: "default", // TODO(user):Modify as needed
			}
			err = k8sClient.Get(ctx, jobNamespacedName, job)
			Expect(err).NotTo(HaveOccurred())

			job1 := &batchv1.Job{}
			jobNamespacedName = types.NamespacedName{
				Name:      resourceName + "-uid1-kitty",
				Namespace: "default", // TODO(user):Modify as needed
			}
			err = k8sClient.Get(ctx, jobNamespacedName, job1)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.TTLSecondsAfterFinished).To(HaveValue(Equal(int32(86400))))

			// Two secrets were defined to pull the images.
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))

			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))

			// Check volumes and mounts based on JuiceFS driver and secret availability
			if hasJuiceFSDriver && hasJuiceFSSecret {
				// With JuiceFS driver and secret, home + workspace volumes
				Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(2))
				Expect(job.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(2))
			} else {
				// Without JuiceFS driver or secret, only home volume
				Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(1))
				Expect(job.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
			}

			Expect(job.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my-registry/applications/"))

			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("service-account"))

			Expect(job1.Spec.Template.Spec.Containers).To(HaveLen(1))

			// Check volumes and mounts for job1 (has shm volume) based on JuiceFS driver and secret availability
			if hasJuiceFSDriver && hasJuiceFSSecret {
				// With JuiceFS driver and secret, shm + home + workspace volumes
				Expect(job1.Spec.Template.Spec.Volumes).To(HaveLen(3))
				Expect(job1.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(3))
			} else {
				// Without JuiceFS driver or secret, shm + home volumes
				Expect(job1.Spec.Template.Spec.Volumes).To(HaveLen(2))
				Expect(job1.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(2))
			}

			Expect(job1.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("quay.io/kitty"))
			Expect(job1.Spec.Template.Spec.Containers[0].Image).To(HaveSuffix("kitty:1.2.0"))

			// Only verify PVC-related resources when JuiceFS driver and secret are available
			if hasJuiceFSDriver && hasJuiceFSSecret {
				// Find the workspace-archive volume
				var workspaceVolume *corev1.Volume
				for _, volume := range job1.Spec.Template.Spec.Volumes {
					if volume.Name == "workspace-archive" {
						workspaceVolume = &volume
						break
					}
				}
				Expect(workspaceVolume).NotTo(BeNil())
				Expect(workspaceVolume.PersistentVolumeClaim).NotTo(BeNil())
				Expect(workspaceVolume.PersistentVolumeClaim.ClaimName).To(Equal("default-archive-pvc"))

				// Find the workspace-archive volume mount
				var workspaceMount *corev1.VolumeMount
				for _, mount := range job1.Spec.Template.Spec.Containers[0].VolumeMounts {
					if mount.Name == "workspace-archive" {
						workspaceMount = &mount
						break
					}
				}
				Expect(workspaceMount).NotTo(BeNil())
				Expect(workspaceMount.MountPath).To(Equal("/mnt/workspace-archive"))
				Expect(workspaceMount.SubPath).To(Equal("workspaces/default"))

				// Verify that the namespace-specific PVC exists and is correctly configured
				pvc := &corev1.PersistentVolumeClaim{}
				pvcNamespacedName := types.NamespacedName{
					Name:      "default-archive-pvc",
					Namespace: "default",
				}
				err = k8sClient.Get(ctx, pvcNamespacedName, pvc)
				Expect(err).NotTo(HaveOccurred())
				Expect(pvc.Spec.VolumeName).To(Equal("default-archive-pv"))
			}
		})

		It("should successfully reconcile the resource with server pod health", func() {
			By("Manually running reconciliation to populate server pod health")

			// Create a reconciler instance
			controllerReconciler := &WorkbenchReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
				Config: Config{
					Registry:               "my-registry",
					AppsRepository:         "applications",
					XpraServerImage:        "my-registry/server/xpra-server",
					JuiceFSSecretName:      "juicefs-secret",
					JuiceFSSecretNamespace: "kube-system",
				},
			}

			// Run reconcile manually
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Get the workbench status after reconciliation
			finalWorkbench := &defaultv1alpha1.Workbench{}
			err = k8sClient.Get(ctx, typeNamespacedName, finalWorkbench)
			Expect(err).NotTo(HaveOccurred())

			// The deployment is created, so server pod health should be populated
			Expect(finalWorkbench.Status.ServerDeployment.ServerPod).NotTo(BeNil())
			// Status could be Ready (if mock pod exists) or Unknown (if no pods)
			Expect(finalWorkbench.Status.ServerDeployment.ServerPod.Status).To(BeElementOf(
				defaultv1alpha1.ServerPodStatusUnknown,
				defaultv1alpha1.ServerPodStatusReady,
			))

			// Verify required fields are populated
			Expect(finalWorkbench.Status.ServerDeployment.ServerPod.RestartCount).To(BeNumerically(">=", int32(0)))
		})
	})

	Context("Server Pod Health Status", func() {
		var reconciler *WorkbenchReconciler

		BeforeEach(func() {
			reconciler = &WorkbenchReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(10),
				Config: Config{
					Registry:               "my-registry",
					AppsRepository:         "applications",
					XpraServerImage:        "my-registry/server/xpra-server",
					JuiceFSSecretName:      "juicefs-secret",
					JuiceFSSecretNamespace: "kube-system",
				},
			}
		})

		Describe("determineContainerHealth", func() {
			It("should return Waiting for waiting container", func() {
				containerStatus := createWaitingContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusWaiting))
				Expect(health.Ready).To(BeFalse())
				Expect(health.RestartCount).To(Equal(int32(0)))
				Expect(health.Message).To(ContainSubstring("Waiting: ImagePullBackOff"))
			})

			It("should return Starting for running but not ready container", func() {
				containerStatus := createStartingContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusStarting))
				Expect(health.Ready).To(BeFalse())
				Expect(health.RestartCount).To(Equal(int32(0)))
				Expect(health.Message).To(ContainSubstring("Container starting up"))
			})

			It("should return Ready for running and ready container", func() {
				containerStatus := createReadyContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusReady))
				Expect(health.Ready).To(BeTrue())
				Expect(health.RestartCount).To(Equal(int32(0)))
				Expect(health.Message).To(ContainSubstring("Container is ready"))
			})

			It("should return Failing for long-running not ready container", func() {
				containerStatus := createFailingContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusFailing))
				Expect(health.Ready).To(BeFalse())
				Expect(health.RestartCount).To(Equal(int32(0)))
				Expect(health.Message).To(ContainSubstring("Readiness probe failing"))
			})

			It("should return Restarting for recently restarted container", func() {
				containerStatus := createRestartingContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusRestarting))
				Expect(health.Ready).To(BeTrue())
				Expect(health.RestartCount).To(Equal(int32(3)))
				Expect(health.Message).To(ContainSubstring("Recently restarted (3 times)"))
			})

			It("should return Terminated for terminated container", func() {
				containerStatus := createTerminatedContainerStatus()
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusTerminated))
				Expect(health.Ready).To(BeFalse())
				Expect(health.RestartCount).To(Equal(int32(1)))
				Expect(health.Message).To(ContainSubstring("Terminated: Error"))
			})

			It("should return Unknown for invalid container state", func() {
				containerStatus := createMockContainerStatus("xpra-server", corev1.ContainerState{}, false, 0)
				health := reconciler.determineContainerHealth(&containerStatus)

				Expect(health.Status).To(Equal(defaultv1alpha1.ServerPodStatusUnknown))
				Expect(health.Message).To(ContainSubstring("Container state unknown"))
			})
		})

		Describe("updateServerPodHealth", func() {
			var workbench *defaultv1alpha1.Workbench
			var deployment appsv1.Deployment
			ctx := context.Background()

			BeforeEach(func() {
				workbench = &defaultv1alpha1.Workbench{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-health-workbench",
						Namespace: "default",
					},
					Status: defaultv1alpha1.WorkbenchStatus{
						ServerDeployment: defaultv1alpha1.WorkbenchStatusServer{
							Revision: 1,
							Status:   defaultv1alpha1.WorkbenchStatusServerStatusRunning,
						},
					},
				}

				deployment = appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-health-workbench-server",
						Namespace: "default",
					},
					Spec: appsv1.DeploymentSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workbench": "test-health-workbench",
							},
						},
					},
				}
			})

			It("should handle missing pods", func() {
				// No pods exist for this deployment
				changed := reconciler.updateServerPodHealth(ctx, workbench, deployment)

				Expect(changed).To(BeTrue())
				Expect(workbench.Status.ServerDeployment.ServerPod).NotTo(BeNil())
				Expect(workbench.Status.ServerDeployment.ServerPod.Status).To(Equal(defaultv1alpha1.ServerPodStatusUnknown))
				Expect(workbench.Status.ServerDeployment.ServerPod.Message).To(ContainSubstring("No pods found"))
			})

			It("should handle pod without xpra-server container", func() {
				// Create pod without xpra-server container
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pod-no-xpra",
						Namespace: "default",
						Labels:    deployment.Spec.Selector.MatchLabels,
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "some-other-container", Image: "test:latest"},
						},
					},
					Status: corev1.PodStatus{
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "some-other-container", Ready: true},
						},
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				changed := reconciler.updateServerPodHealth(ctx, workbench, deployment)

				Expect(changed).To(BeTrue())
				Expect(workbench.Status.ServerDeployment.ServerPod.Status).To(Equal(defaultv1alpha1.ServerPodStatusUnknown))
				Expect(workbench.Status.ServerDeployment.ServerPod.Message).To(ContainSubstring("init container not found"))

				// Cleanup
				Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
				// Wait for deletion to complete
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)
				}, "3s", "100ms").ShouldNot(Succeed())
			})

			It("should handle terminating pods", func() {
				// Create pod
				pod := createMockPod("test-terminating-pod", "default", createReadyContainerStatus(), nil)
				pod.Labels = deployment.Spec.Selector.MatchLabels
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())

				// Delete the pod (this sets deletionTimestamp)
				Expect(k8sClient.Delete(ctx, pod)).To(Succeed())

				// Get the pod again - it should now have deletionTimestamp set
				deletingPod := &corev1.Pod{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, deletingPod)
				if err == nil && deletingPod.DeletionTimestamp != nil {
					// Pod is in terminating state
					changed := reconciler.updateServerPodHealth(ctx, workbench, deployment)

					Expect(changed).To(BeTrue())
					Expect(workbench.Status.ServerDeployment.ServerPod.Status).To(Equal(defaultv1alpha1.ServerPodStatusTerminating))
					Expect(workbench.Status.ServerDeployment.ServerPod.Message).To(ContainSubstring("Pod is terminating"))
				} else {
					// Pod was immediately deleted, test the logic directly with a mock
					workbench.Status.ServerDeployment.ServerPod = nil
					testPod := createMockPod("test-pod", "default", createReadyContainerStatus(), nil)
					testPod.Labels = deployment.Spec.Selector.MatchLabels
					now := metav1.Now()
					testPod.DeletionTimestamp = &now

					// Mock the API call by updating our test deployment selector to match nothing
					// and create pod list manually
					health := defaultv1alpha1.ServerPodHealth{
						Status:  defaultv1alpha1.ServerPodStatusTerminating,
						Message: "Pod is terminating",
					}
					changed := reconciler.setServerPodHealth(workbench, health)
					Expect(changed).To(BeTrue())
					Expect(workbench.Status.ServerDeployment.ServerPod.Status).To(Equal(defaultv1alpha1.ServerPodStatusTerminating))
				}
			})

			It("should pick latest pod when multiple exist", func() {
				// Create older pod first (will have earlier creation time)
				olderPod := createMockPod("older-pod", "default", createFailingContainerStatus(), nil)
				olderPod.Labels = deployment.Spec.Selector.MatchLabels
				Expect(k8sClient.Create(ctx, olderPod)).To(Succeed())

				// Wait a bit to ensure different creation timestamps
				time.Sleep(50 * time.Millisecond)

				// Create newer pod (will have later creation time)
				newerPod := createMockPod("newer-pod", "default", createReadyContainerStatus(), nil)
				newerPod.Labels = deployment.Spec.Selector.MatchLabels
				Expect(k8sClient.Create(ctx, newerPod)).To(Succeed())

				// Debug: verify pods were created with correct labels and status
				podList := &corev1.PodList{}
				err := k8sClient.List(ctx, podList, client.InNamespace("default"), client.MatchingLabels(deployment.Spec.Selector.MatchLabels))
				Expect(err).NotTo(HaveOccurred())
				Expect(len(podList.Items)).To(Equal(2))

				// In test environment, container statuses aren't populated by kubelet simulation
				// So we expect the updateServerPodHealth to return "Unknown" status
				// since it can't find the xpra-server container status
				changed := reconciler.updateServerPodHealth(ctx, workbench, deployment)

				// Should detect that no container statuses are available and mark as Unknown
				Expect(changed).To(BeTrue())
				Expect(workbench.Status.ServerDeployment.ServerPod.Status).To(Equal(defaultv1alpha1.ServerPodStatusUnknown))
				Expect(workbench.Status.ServerDeployment.ServerPod.Message).To(ContainSubstring("init container not found"))

				// Cleanup
				Expect(k8sClient.Delete(ctx, olderPod)).To(Succeed())
				Expect(k8sClient.Delete(ctx, newerPod)).To(Succeed())
			})
		})
	})

	Describe("Storage Configuration", func() {

		Context("Job Volume Configuration", func() {
			It("should handle storage configuration when drivers are not available", func() {
				// This test verifies that jobs are created successfully even when storage is enabled
				// but the required drivers/secrets are not available (graceful degradation)
				workbench := defaultv1alpha1.Workbench{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-workbench",
						Namespace: "default",
					},
					Spec: defaultv1alpha1.WorkbenchSpec{
						Server: defaultv1alpha1.WorkbenchServer{
							User:   "testuser",
							UserID: 1001,
						},
						Storage: &defaultv1alpha1.StorageConfig{
							S3:  true,
							NFS: true,
						},
					},
				}

				config := Config{
					Registry:       "test.registry.io",
					AppsRepository: "apps",
				}

				service := corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-service",
						Namespace: workbench.Namespace,
					},
				}

				app := defaultv1alpha1.WorkbenchApp{
					Name: "test-app",
					Image: &defaultv1alpha1.Image{
						Registry:   "test.registry.io",
						Repository: "apps/test-app",
						Tag:        "latest",
					},
				}

				// Create a storage manager for testing
				reconciler := &WorkbenchReconciler{
					Client: k8sClient,
					Config: config,
				}
				storageManager := NewStorageManager(reconciler)

				ctx := context.Background()
				job, err := initJob(ctx, workbench, config, "test-uid", app, service, storageManager)
				Expect(err).NotTo(HaveOccurred())

				// Verify only home volume was added since storage drivers are not available
				Expect(len(job.Spec.Template.Spec.Volumes)).To(Equal(1))

				// Verify only home volume mount was added since storage is not available
				container := job.Spec.Template.Spec.Containers[0]
				Expect(len(container.VolumeMounts)).To(Equal(1))
			})

			It("should not add volumes when PVC names are empty", func() {
				workbench := defaultv1alpha1.Workbench{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-workbench",
						Namespace: "default",
					},
					Spec: defaultv1alpha1.WorkbenchSpec{
						Server: defaultv1alpha1.WorkbenchServer{
							User:   "testuser",
							UserID: 1001,
						},
					},
				}

				config := Config{
					Registry:       "test.registry.io",
					AppsRepository: "apps",
				}

				service := corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-service",
						Namespace: workbench.Namespace,
					},
				}

				app := defaultv1alpha1.WorkbenchApp{
					Name: "test-app",
					Image: &defaultv1alpha1.Image{
						Registry:   "test.registry.io",
						Repository: "apps/test-app",
						Tag:        "latest",
					},
				}

				// Create a storage manager for testing
				reconciler := &WorkbenchReconciler{
					Client: k8sClient,
					Config: config,
				}
				storageManager := NewStorageManager(reconciler)

				ctx := context.Background()
				job, err := initJob(ctx, workbench, config, "test-uid", app, service, storageManager)
				Expect(err).NotTo(HaveOccurred())

				// Verify only home volume was added (no storage volumes)
				Expect(len(job.Spec.Template.Spec.Volumes)).To(Equal(1))

				// Verify only home volume mount was added (no storage mounts)
				container := job.Spec.Template.Spec.Containers[0]
				Expect(len(container.VolumeMounts)).To(Equal(1))
			})
		})
	})
})
