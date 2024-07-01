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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// WorkbenchReconciler reconciles a Workbench object
type WorkbenchReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// finalizer used to control the clean up the deployments.
const finalizer = "default.k8s.chorus-tre.ch/finalizer"

// matchingLabel to search for sub-resources.
const matchingLabel = "xpra-server"

// deleteExternalResources removes the underlying Deployment(s).
func (r *WorkbenchReconciler) deleteExternalResources(ctx context.Context, workbench *defaultv1alpha1.Workbench) (int, error) {
	log := log.FromContext(ctx)

	// Find all the deployments linked with the workbench.
	deploymentList := appsv1.DeploymentList{}

	err := r.List(
		ctx,
		&deploymentList,
		client.MatchingLabels{matchingLabel: workbench.Name},
	)
	if err != nil {
		return 0, err
	}

	// FIXME: use DeleteAllOf instead.
	// Delete them.
	for _, item := range deploymentList.Items {
		r.Recorder.Event(
			workbench,
			"Normal",
			"DeletingDeployment",
			fmt.Sprintf(
				"Deleting deployment %q from the namespace %q",
				item.Name,
				item.Namespace,
			),
		)

		log.V(1).Info("Delete deployment", "deployment", item.Name)
		if err := r.Delete(ctx, &item); err != nil {
			return 0, err
		}
	}

	return len(deploymentList.Items), nil
}

// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=default.chorus-tre.ch,resources=workbenches/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments/status,verbs=get
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *WorkbenchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	log.V(1).Info("Reconcile", "what", req.NamespacedName)

	// Fetch the workbench to reconcile.
	workbench := defaultv1alpha1.Workbench{}
	if err := r.Get(ctx, req.NamespacedName, &workbench); err != nil {
		// Not found means it's been deleted.
		if !errors.IsNotFound(err) {
			log.Error(err, "unable to fetch the workbench")
		}

		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Manage deletion and finalizers.
	containsFinalizer := controllerutil.ContainsFinalizer(&workbench, finalizer)

	if !workbench.DeletionTimestamp.IsZero() {
		// Object has been deleted
		if containsFinalizer {
			// It first removes the sub-resources, then the finalizer.
			count, err := r.deleteExternalResources(ctx, &workbench)
			if err != nil {
				return ctrl.Result{}, err
			}

			// We will get a resource name may not be empty error otherwise.
			if count > 0 {
				return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
			}

			finalizersUpdated := controllerutil.RemoveFinalizer(&workbench, finalizer)
			if finalizersUpdated {
				if err := r.Update(ctx, &workbench); err != nil {
					return ctrl.Result{}, err
				}
			}
		}

		// Stop reconciliation as the object is being deleted.
		return ctrl.Result{}, nil
	}

	// verify that the finalizer exists.
	if !containsFinalizer {
		finalizersUpdated := controllerutil.AddFinalizer(&workbench, finalizer)
		if finalizersUpdated {
			if err := r.Update(ctx, &workbench); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// The deployment of Xpra
	deployment := initDeployment(workbench)
	deploymentNamespacedName := types.NamespacedName{
		Name:      deployment.Name,
		Namespace: deployment.Namespace,
	}

	foundDeployment := appsv1.Deployment{}
	err := r.Get(ctx, deploymentNamespacedName, &foundDeployment)
	if err != nil {
		if !errors.IsNotFound(err) {
			log.V(1).Error(err, "Deployment is not (not) found.")
			return ctrl.Result{}, err
		}

		log.V(1).Info("Creating the deployment", "deployment", deployment.Name)

		// Link the deployment with the Workbench resource such that we can reconcile it
		// when it's being changed.
		if err := controllerutil.SetControllerReference(&workbench, &deployment, r.Scheme); err != nil {
			log.V(1).Error(err, "Error setting the reference")
			return ctrl.Result{}, err
		}

		r.Recorder.Event(
			&workbench,
			"Normal",
			"CreatingDeployment",
			fmt.Sprintf(
				"Creating deployment %q into the namespace %q",
				deployment.Name,
				deployment.Namespace,
			),
		)

		if err := r.Create(ctx, &deployment); err != nil {
			log.V(1).Error(err, "Error creating the deployment")
			// It's probably has already been created.
			// FIXME check that it's indeed the case.
		}

		// It's been created with success, don't loop straight away.
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// TODO: to properly follow the deployment we have to dig into the replicaset
	// via metadata.annotations."deployment.kubernetes.io/revision"
	// which is also present on the replica. Then the pods, which can be found via
	// the labels, has said replicas as its owner.
	statusUpdated := (&workbench).UpdateStatusFromDeployment(foundDeployment)
	if statusUpdated {
		if err := r.Status().Update(ctx, &workbench); err != nil {
			log.V(1).Error(err, "Unable to update the WorkbenchStatus")
		}
	}

	// Update the existing deployment with the model one.
	updated := updateDeployment(deployment, &foundDeployment)

	if updated {
		log.V(1).Info("Updating Deployment", "deployment", foundDeployment.Name)

		r.Recorder.Event(
			&workbench,
			"Normal",
			"UpdatingDeployment",
			fmt.Sprintf(
				"Updating deployment %q into the namespace %q",
				deployment.Name,
				deployment.Namespace,
			),
		)

		err2 := r.Update(ctx, &foundDeployment)
		if err2 != nil {
			log.V(1).Error(err2, "Unable to update the deployment")
			return ctrl.Result{}, err2
		}
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
