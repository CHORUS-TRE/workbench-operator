/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// WorkbenchReconciler reconciles a Workbench object
type WorkbenchReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Workbench object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.18.2/pkg/reconcile
func (r *WorkbenchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(1).Info("Reconcile it", "what", req.NamespacedName)

	workbench := defaultv1alpha1.Workbench{}

	if err := r.Get(ctx, req.NamespacedName, &workbench); err != nil {
		// Not found means it's been deleted.
		if !errors.IsNotFound(err) {
			log.Error(err, "unable to fetch the workbench")
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	finalizer := "default.k8s.chorus-tre.ch/finalizer"
	containsFinalizer := controllerutil.ContainsFinalizer(&workbench, finalizer)

	if workbench.DeletionTimestamp.IsZero() {
		// Object is not being deleted.
		// verify that the finalizer exists.
		if !containsFinalizer {
			controllerutil.AddFinalizer(&workbench, finalizer)
			if err := r.Update(ctx, &workbench); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		if containsFinalizer {
			// It first removes the sub-resources, then the finalizer.
			deploymentList := appsv1.DeploymentList{}
			err := r.List(
				ctx,
				&deploymentList,
				client.MatchingLabels{"xpra-server": workbench.Name},
			)
			if err != nil {
				return ctrl.Result{}, err
			}

			for _, item := range deploymentList.Items {
				log.V(1).Info("Delete deployment", "deployment", item.Name)
				if err := r.Delete(ctx, &item); err != nil {
					return ctrl.Result{}, err
				}
			}

			if len(deploymentList.Items) > 0 {
				// Wait for the deployments to be cleaned up.
				log.V(1).Info("Deployments are hanging", "count", len(deploymentList.Items))
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			controllerutil.RemoveFinalizer(&workbench, finalizer)

			if err := r.Update(ctx, &workbench); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the object is being deleted.
		return ctrl.Result{}, nil
	}

	deployment := &appsv1.Deployment{}
	deployment.Name = workbench.Name
	deployment.Namespace = workbench.Namespace

	// Labels
	labels := map[string]string{
		"xpra-server": workbench.Name,
	}

	deployment.Labels = labels
	deployment.Spec.Selector = &metav1.LabelSelector{
		MatchLabels: labels,
	}
	deployment.Spec.Template.Labels = labels

	// Shared by the containers
	volume := corev1.Volume{
		Name: "x11-unix",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
	volumeMounts := []corev1.VolumeMount{
		{
			Name:      volume.Name,
			MountPath: "/tmp/.X11-unix",
		},
	}

	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{volume}

	always := corev1.ContainerRestartPolicyAlways

	deployment.Spec.Template.Spec.InitContainers = []corev1.Container{
		{
			Name:            "xpra-server-bind",
			Image:           "alpine/socat:1.8.0.0",
			ImagePullPolicy: "IfNotPresent",
			RestartPolicy:   &always,
			Ports: []corev1.ContainerPort{
				{
					Name:          "x11-socket",
					ContainerPort: 6080,
				},
			},
			Args: []string{
				"TCP-LISTEN:6080,fork,bind=0.0.0.0",
				"UNIX-CONNECT:/tmp/.X11-unix/X80",
			},
			VolumeMounts: volumeMounts,
		},
	}

	// TODO: put default values via the admission webhook.
	serverVersion := workbench.Spec.ServerVersion
	if serverVersion == "" {
		serverVersion = "latest"
	}
	// TODO: allow the registry to be specifiec as well.
	serverImage := fmt.Sprintf("registry.build.chorus-tre.local/xpra-server:%s", serverVersion)
	deployment.Spec.Template.Spec.Containers = []corev1.Container{
		{
			Name:            "xpra-server",
			Image:           serverImage,
			ImagePullPolicy: "IfNotPresent",
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: 8080,
				},
			},
			Env: []corev1.EnvVar{
				{
					Name:  "CARD",
					Value: "",
				},
			},
			VolumeMounts: volumeMounts,
		},
	}

	deploymentNamespacedName := types.NamespacedName{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
	}

	foundDeployment := appsv1.Deployment{}
	err := r.Get(ctx, deploymentNamespacedName, &foundDeployment)

	if err != nil && errors.IsNotFound(err) {
		log.V(1).Info("Creating the deployment", "deployment", deployment.Name)

		// Link the deployment with the Workbench resource such that we can reconcile it
		// when it's being changed.
		if err := controllerutil.SetControllerReference(&workbench, deployment, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}

		if err := r.Create(ctx, deployment); err != nil {
			return ctrl.Result{}, err
		}

		// It's been created with success.
		return ctrl.Result{}, nil
	}

	if err == nil {
		initContainers := foundDeployment.Spec.Template.Spec.InitContainers
		if len(initContainers) != 1 {
			foundDeployment.Spec.Template.Spec.InitContainers = deployment.Spec.Template.Spec.InitContainers
		}

		containers := foundDeployment.Spec.Template.Spec.Containers
		if len(containers) != 1 {
			foundDeployment.Spec.Template.Spec.Containers = deployment.Spec.Template.Spec.Containers
		}

		// Use the new server image.
		if containers[0].Image != serverImage {
			foundDeployment.Spec.Template.Spec.Containers[0].Image = serverImage
		}

		log.V(1).Info("Updating Deployment", "deployment", foundDeployment.Name)

		err2 := r.Update(ctx, &foundDeployment)
		return ctrl.Result{}, err2
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkbenchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&defaultv1alpha1.Workbench{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
