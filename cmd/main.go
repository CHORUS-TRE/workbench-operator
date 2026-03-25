package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
	"github.com/CHORUS-TRE/workbench-operator/internal/controller"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// internalServiceFlag is a repeated flag that accumulates namespace/fqdn:port[,port...] entries.
type internalServiceFlag []controller.InternalService

func (f internalServiceFlag) String() string {
	parts := make([]string, 0, len(f))
	for _, svc := range f {
		parts = append(parts, svc.Namespace+"/"+svc.FQDN+":"+strings.Join(svc.Ports, ","))
	}
	return strings.Join(parts, " ")
}

func (f *internalServiceFlag) Set(s string) error {
	slashIdx := strings.Index(s, "/")
	if slashIdx < 1 {
		return fmt.Errorf("global-internal-service %q must be in namespace/fqdn:port[,port...] format", s)
	}
	namespace := s[:slashIdx]
	rest := s[slashIdx+1:]

	idx := strings.LastIndex(rest, ":")
	if idx < 1 {
		return fmt.Errorf("global-internal-service %q must be in namespace/fqdn:port[,port...] format", s)
	}
	fqdn := strings.ToLower(strings.TrimSpace(rest[:idx]))
	if err := controller.ValidateFQDNs([]string{fqdn}); err != nil {
		return fmt.Errorf("global-internal-service %q contains invalid FQDN %q: %w", s, fqdn, err)
	}
	rawPorts := strings.Split(rest[idx+1:], ",")
	ports := make([]string, 0, len(rawPorts))
	for _, p := range rawPorts {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("global-internal-service %q contains empty port", s)
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("global-internal-service %q contains invalid port %q: must be a number between 1 and 65535", s, p)
		}
		ports = append(ports, p)
	}
	*f = append(*f, controller.InternalService{Namespace: namespace, FQDN: fqdn, Ports: ports})
	return nil
}

// labelFlag is a repeated flag that accumulates key=value pairs into a map.
type labelFlag map[string]string

func (f labelFlag) String() string {
	pairs := make([]string, 0, len(f))
	for k, v := range f {
		pairs = append(pairs, k+"="+v)
	}
	return strings.Join(pairs, ",")
}

func (f labelFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("pvc-label %q must be in key=value format", s)
	}
	f[k] = v
	return nil
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(defaultv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var registry string
	var appsRepository string
	var servicesRepository string
	var xpraServerImage string
	var initContainerImage string
	var socatImage string
	var juiceFSSecretName string
	var juiceFSSecretNamespace string
	var nfsSecretName string
	var nfsSecretNamespace string
	var localStorageEnabled bool
	var localStorageHostPath string
	var debugModeEnabled bool
	var workbenchPriorityClassName string
	var applicationPriorityClassName string
	var workbenchStartupTimeout int
	var applicationStartupTimeout int
	var workbenchCPULimit string
	var workbenchMemoryLimit string
	var workbenchCPURequest string
	var workbenchMemoryRequest string
	pvcLabels := labelFlag{}
	globalInternalServices := internalServiceFlag{}
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metric endpoint binds to. "+
		"Use the port :8080. If not set, it will be 0 in order to disable the metrics server")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", false,
		"If set the metrics endpoint is served securely")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.StringVar(&registry, "registry", "harbor.build.chorus-tre.local", "The hostname of the OCI registry")
	flag.StringVar(&appsRepository, "apps-repository", "apps", "The repository holding the apps")
	flag.StringVar(&servicesRepository, "services-repository", "services", "The OCI project holding the Helm charts for workspace services")
	flag.StringVar(&xpraServerImage, "xpra-server-image", "", "Xpra server OCI image name (version is part of the CRD)")
	flag.StringVar(&initContainerImage, "init-container-image", "", "Init container OCI image name (no version)")
	flag.StringVar(&socatImage, "socat-image", "", "socat OCI image (please specify the version)")
	flag.StringVar(&juiceFSSecretName, "juicefs-secret-name", "juicefs-secret", "Name of the JuiceFS secret")
	flag.StringVar(&juiceFSSecretNamespace, "juicefs-secret-namespace", "kube-system", "Namespace of the JuiceFS secret")
	flag.StringVar(&nfsSecretName, "nfs-secret-name", "nfs-secret", "Name of the NFS secret")
	flag.StringVar(&nfsSecretNamespace, "nfs-secret-namespace", "kube-system", "Namespace of the NFS secret")
	flag.BoolVar(&localStorageEnabled, "local-storage-enabled", false, "Enable local storage provider for development (uses hostPath volumes)")
	flag.StringVar(&localStorageHostPath, "local-storage-host-path", "/tmp/workbench-local-storage", "Host path for local storage volumes")
	flag.BoolVar(&debugModeEnabled, "debug-mode-enabled", false, "Enable debug mode for all workbenches (elevated privileges for debugging). Only use for local development.")
	flag.StringVar(&workbenchPriorityClassName, "workbench-priority-class-name", "", "Priority class name to set on Workbench pods")
	flag.StringVar(&applicationPriorityClassName, "application-priority-class-name", "", "Priority class name to set on Application pods")
	flag.IntVar(&workbenchStartupTimeout, "workbench-startup-timeout", 600, "Timeout in seconds for the xpra server deployment to become ready")
	flag.IntVar(&applicationStartupTimeout, "application-startup-timeout", 600, "Timeout in seconds for app jobs to become ready (covers image pull, init, scheduling). Once running, no timeout applies.")
	flag.StringVar(&workbenchCPULimit, "workbench-cpu-limit", "", "Default CPU limit for the workbench server container (e.g. 1000m)")
	flag.StringVar(&workbenchMemoryLimit, "workbench-memory-limit", "", "Default memory limit for the workbench server container (e.g. 512Mi)")
	flag.StringVar(&workbenchCPURequest, "workbench-cpu-request", "", "Default CPU request for the workbench server container (e.g. 100m)")
	flag.StringVar(&workbenchMemoryRequest, "workbench-memory-request", "", "Default memory request for the workbench server container (e.g. 256Mi)")
	flag.Var(pvcLabels, "pvc-label", "Label to add to every PVC created by the operator, in key=value format (can be repeated)")
	flag.Var(&globalInternalServices, "global-internal-service", "Platform-internal service always reachable from workspaces, in namespace/fqdn:port[,port...] format (can be repeated)")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Log local storage configuration with safety warning
	if localStorageEnabled {
		setupLog.Info("LOCAL STORAGE ENABLED - Development Mode Only",
			"path", localStorageHostPath,
			"warning", "Local storage uses hostPath volumes and should ONLY be used for local development. Do not use in production!")
	}

	// Log debug mode configuration with security warning
	if debugModeEnabled {
		setupLog.Info("DEBUG MODE ENABLED - Development Mode Only",
			"warning", "Debug mode grants elevated privileges (root access, SYS_PTRACE, SYS_ADMIN) to all workbenches. NEVER use in production!")
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	webhookServer := webhook.NewServer(webhook.Options{
		TLSOpts: tlsOpts,
	})

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsAddr,
			SecureServing: secureMetrics,
			TLSOpts:       tlsOpts,
		},
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "744cc179.chorus-tre.ch",
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Validate internal services against the cluster before starting.
	// Uses a direct (non-cached) client so it works before mgr.Start().
	if len(globalInternalServices) > 0 {
		directClient, err := client.New(mgr.GetConfig(), client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "unable to create client for internal service validation")
			os.Exit(1)
		}
		if err := controller.ValidateInternalServices(context.Background(), directClient, []controller.InternalService(globalInternalServices)); err != nil {
			setupLog.Error(err, "internal service validation failed")
			os.Exit(1)
		}
	}

	// Build default workbench resources from flags (nil if no flags are set)
	var workbenchDefaultResources *corev1.ResourceRequirements
	if workbenchCPULimit != "" || workbenchMemoryLimit != "" || workbenchCPURequest != "" || workbenchMemoryRequest != "" {
		workbenchDefaultResources = &corev1.ResourceRequirements{}
		if workbenchCPULimit != "" || workbenchMemoryLimit != "" {
			workbenchDefaultResources.Limits = corev1.ResourceList{}
			if workbenchCPULimit != "" {
				workbenchDefaultResources.Limits[corev1.ResourceCPU] = resource.MustParse(workbenchCPULimit)
			}
			if workbenchMemoryLimit != "" {
				workbenchDefaultResources.Limits[corev1.ResourceMemory] = resource.MustParse(workbenchMemoryLimit)
			}
		}
		if workbenchCPURequest != "" || workbenchMemoryRequest != "" {
			workbenchDefaultResources.Requests = corev1.ResourceList{}
			if workbenchCPURequest != "" {
				workbenchDefaultResources.Requests[corev1.ResourceCPU] = resource.MustParse(workbenchCPURequest)
			}
			if workbenchMemoryRequest != "" {
				workbenchDefaultResources.Requests[corev1.ResourceMemory] = resource.MustParse(workbenchMemoryRequest)
			}
		}
	}

	if err = (&controller.WorkbenchReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("workbench-controller"),
		Config: controller.Config{
			Registry:                     registry,
			AppsRepository:               appsRepository,
			SocatImage:                   socatImage,
			XpraServerImage:              xpraServerImage,
			InitContainerImage:           initContainerImage,
			JuiceFSSecretName:            juiceFSSecretName,
			JuiceFSSecretNamespace:       juiceFSSecretNamespace,
			NFSSecretName:                nfsSecretName,
			NFSSecretNamespace:           nfsSecretNamespace,
			LocalStorageEnabled:          localStorageEnabled,
			LocalStorageHostPath:         localStorageHostPath,
			DebugModeEnabled:             debugModeEnabled,
			WorkbenchPriorityClassName:   workbenchPriorityClassName,
			ApplicationPriorityClassName: applicationPriorityClassName,
			WorkbenchStartupTimeout:      workbenchStartupTimeout,
			ApplicationStartupTimeout:    applicationStartupTimeout,
			WorkbenchDefaultResources:    workbenchDefaultResources,
			PVCLabels:                    map[string]string(pvcLabels),
		},
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workbench")
		os.Exit(1)
	}
	if err = (&controller.WorkspaceReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		Recorder:               mgr.GetEventRecorderFor("workspace-controller"),
		RestConfig:             mgr.GetConfig(),
		Registry:               registry,
		ServicesRepository:     servicesRepository,
		GlobalInternalServices: []controller.InternalService(globalInternalServices),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Workspace")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
