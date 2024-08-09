package controller

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

func initService(workbench defaultv1alpha1.Workbench) corev1.Service {
	service := corev1.Service{}
	service.Name = workbench.Name
	service.Namespace = workbench.Namespace

	// Labels
	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	service.Labels = labels
	service.Spec.Selector = labels

	service.Spec.Ports = []corev1.ServicePort{
		{
			Port:       8080,
			TargetPort: intstr.FromString("http"),
			Protocol:   "TCP",
			Name:       "http",
		},
		// Using the named port seems to break.
		// https://github.com/projectcalico/calico/issues/8881
		{
			Port:       6080,
			TargetPort: intstr.FromInt(6080), // FIXME: should be x11-socket
			Protocol:   "TCP",
			Name:       "x11-socket",
		},
	}

	// Default type for internal usage.
	service.Spec.Type = "ClusterIP"

	return service
}
