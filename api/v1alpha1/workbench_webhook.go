package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var workbenchlog = logf.Log.WithName("workbench-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (r *Workbench) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(r).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-default-chorus-tre-ch-v1alpha1-workbench,mutating=true,failurePolicy=fail,sideEffects=None,groups=default.chorus-tre.ch,resources=workbenches,verbs=create;update,versions=v1alpha1,name=mworkbench.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Workbench{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (r *Workbench) Default() {
	workbenchlog.Info("default", "name", r.Name)

	if r.Spec.Server.Version == "" {
		r.Spec.Server.Version = "latest"
	}

	for index, app := range r.Spec.Apps {
		if app.Version == "" {
			r.Spec.Apps[index].Version = "latest"
		}
		if app.State == "" {
			r.Spec.Apps[index].State = WorkbenchAppStateRunning
		}
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-default-chorus-tre-ch-v1alpha1-workbench,mutating=false,failurePolicy=fail,sideEffects=None,groups=default.chorus-tre.ch,resources=workbenches,verbs=create;update,versions=v1alpha1,name=vworkbench.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Workbench{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *Workbench) ValidateCreate() (admission.Warnings, error) {
	workbenchlog.Info("validate create", "name", r.Name)

	// TODO(user): fill in your validation logic upon object creation.
	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *Workbench) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	workbenchlog.Info("validate update", "name", r.Name)

	// TODO(user): fill in your validation logic upon object update.
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *Workbench) ValidateDelete() (admission.Warnings, error) {
	workbenchlog.Info("validate delete", "name", r.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
