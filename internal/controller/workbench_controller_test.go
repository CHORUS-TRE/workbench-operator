package controller

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

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
			"1": {
				Name: "wezterm",
			},
			"2": {
				Name: "kitty",
				Image: &defaultv1alpha1.Image{
					Registry:   "quay.io",
					Repository: "kitty/kitty",
					Tag:        "1.2.0",
				},
				ShmSize: &oneGig,
			},
			"3": {
				Name:  "alacritty",
				State: "Stopped",
			},
		}

		workbench.Spec.ImagePullSecrets = []string{
			"secret-1",
			"secret-2",
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
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &WorkbenchReconciler{
				Client:   k8sClient,
				Scheme:   k8sClient.Scheme(),
				Recorder: record.NewFakeRecorder(3),
				Config: Config{
					Registry:        "my-registry",
					AppsRepository:  "applications",
					XpraServerImage: "my-registry/server/xpra-server",
				},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
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
				Name:      resourceName + "-0-wezterm",
				Namespace: "default", // TODO(user):Modify as needed
			}
			err = k8sClient.Get(ctx, jobNamespacedName, job)
			Expect(err).NotTo(HaveOccurred())

			job1 := &batchv1.Job{}
			jobNamespacedName = types.NamespacedName{
				Name:      resourceName + "-1-kitty",
				Namespace: "default", // TODO(user):Modify as needed
			}
			err = k8sClient.Get(ctx, jobNamespacedName, job1)
			Expect(err).NotTo(HaveOccurred())

			Expect(job.Spec.TTLSecondsAfterFinished).To(HaveValue(Equal(int32(86400))))

			// Two secrets were defined to pull the images.
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))

			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.Volumes).To(HaveLen(0))

			Expect(job.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my-registry/applications/"))
			Expect(job.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(0))

			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("service-account"))

			Expect(job1.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job1.Spec.Template.Spec.Volumes).To(HaveLen(1))

			Expect(job1.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("quay.io/kitty"))
			Expect(job1.Spec.Template.Spec.Containers[0].Image).To(HaveSuffix("kitty:1.2.0"))
			Expect(job1.Spec.Template.Spec.Containers[0].VolumeMounts).To(HaveLen(1))
		})
	})
})
