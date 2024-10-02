package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var workbenchlog = logf.Log.WithName("workbench-resource")

// SetupWebhookWithManager will setup the manager to manage the webhooks
func (wb *Workbench) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(wb).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-default-chorus-tre-ch-v1alpha1-workbench,mutating=true,failurePolicy=fail,sideEffects=None,groups=default.chorus-tre.ch,resources=workbenches,verbs=create;update,versions=v1alpha1,name=mworkbench.kb.io,admissionReviewVersions=v1

var _ webhook.Defaulter = &Workbench{}

// Default implements webhook.Defaulter so a webhook will be registered for the type
func (wb *Workbench) Default() {
	workbenchlog.Info("default", "name", wb.Name)

	if wb.Spec.Server.Version == "" {
		wb.Spec.Server.Version = "latest"
	}

	for index, app := range wb.Spec.Apps {
		if app.Version == "" {
			wb.Spec.Apps[index].Version = "latest"
		}
		if app.State == "" {
			wb.Spec.Apps[index].State = WorkbenchAppStateRunning
		}
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-default-chorus-tre-ch-v1alpha1-workbench,mutating=false,failurePolicy=fail,sideEffects=None,groups=default.chorus-tre.ch,resources=workbenches,verbs=create;update,versions=v1alpha1,name=vworkbench.kb.io,admissionReviewVersions=v1

var _ webhook.Validator = &Workbench{}

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
//
// The app name shouldn't be empty, but this is mostly a double check as this validation already exists at the OpenAPI level.
func (wb *Workbench) ValidateCreate() (admission.Warnings, error) {
	workbenchlog.Info("validate create", "name", wb.Name)

	for index, app := range wb.Spec.Apps {
		if app.Name == "" {
			return nil, fmt.Errorf("empty application name at position %d is invalid.", index)
		}
	}

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
//
// Updating a workbench has some constraints as the ordered bag of applications cannot easily
// be modified. E.g. one application cannot move positions into the list, therefor you're not
// allowed to remove any applications, only to stop them. Adding more applications at the end
// of the list is also fine.
func (wb *Workbench) ValidateUpdate(old runtime.Object) (admission.Warnings, error) {
	workbenchlog.Info("validate update", "name", wb.Name)

	oldWorkbench, ok := old.(*Workbench)
	if !ok {
		return nil, fmt.Errorf("expected a Workbench but got a %T", old)
	}

	if len(wb.Spec.Apps) < len(oldWorkbench.Spec.Apps) {
		return nil, fmt.Errorf("apps cannot be removed from a Workbench, only stopped.")
	}

	for index, oldApp := range oldWorkbench.Spec.Apps {
		// As the old is smaller or equal than the wb, this is safe.
		app := wb.Spec.Apps[index]

		if app.Name != oldApp.Name {
			return nil, fmt.Errorf("apps cannot be moved or changed on the fly, stop them, and new ones. App %d should be %q, not %q", index, app.Name, oldApp.Name)
		}
	}

	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (wb *Workbench) ValidateDelete() (admission.Warnings, error) {
	workbenchlog.Info("validate delete", "name", wb.Name)

	// TODO(user): fill in your validation logic upon object deletion.
	return nil, nil
}
