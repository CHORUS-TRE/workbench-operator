package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
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

		workbench.Spec.Apps = []defaultv1alpha1.WorkbenchApp{
			{
				Name: "wezterm",
			},
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
					Registry: "my-registry",
					ImagePullSecrets: []string{
						"secret-1",
						"secret-2",
					},
				},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Verify that a deployment exists.
			deployment := &appsv1.Deployment{}
			err = k8sClient.Get(ctx, typeNamespacedName, deployment)
			Expect(err).NotTo(HaveOccurred())

			// Two secrets were defined to pull the images.
			Expect(deployment.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))

			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(2))

			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my-registry/"))
			Expect(deployment.Spec.Template.Spec.Containers[1].Image).To(HavePrefix("alpine/socat:"))

			// Verify that a service exists
			service := &corev1.Service{}
			err = k8sClient.Get(ctx, typeNamespacedName, service)
			Expect(err).NotTo(HaveOccurred())

			// Verify that a job exists
			job := &batchv1.Job{}
			jobNamespacedName := types.NamespacedName{
				Name:      resourceName + "-0-wezterm",
				Namespace: "default", // TODO(user):Modify as needed
			}
			err = k8sClient.Get(ctx, jobNamespacedName, job)
			Expect(err).NotTo(HaveOccurred())

			// Two secrets were defined to pull the images.
			Expect(job.Spec.Template.Spec.ImagePullSecrets).To(HaveLen(2))

			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))

			Expect(job.Spec.Template.Spec.Containers[0].Image).To(HavePrefix("my-registry/"))
		})
	})
})
