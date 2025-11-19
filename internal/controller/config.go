package controller

// Config holds the global configuration that was given to the controller.
type Config struct {
	// Registry contains the hostname of the server and apps OCI images.
	Registry string
	// AppsRepository holds the repository where to find the applications.
	AppsRepository string
	// SocatImage is the image (with version) to expose X11 on a TCP socket.
	SocatImage string
	// XpraServerImage is the image (no version) used as the server.
	XpraServerImage string
	// InitContainerImage is the image (no version) used for the init container.
	InitContainerImage string
	// JuiceFSSecretName is the name of the JuiceFS secret.
	JuiceFSSecretName string
	// JuiceFSSecretNamespace is the namespace of the JuiceFS secret.
	JuiceFSSecretNamespace string
	// NFSSecretName is the name of the NFS secret.
	NFSSecretName string
	// NFSSecretNamespace is the namespace of the NFS secret.
	NFSSecretNamespace string
	// LocalStorageEnabled enables local storage provider for development
	LocalStorageEnabled bool
	// LocalStorageHostPath is the host path for local storage volumes
	LocalStorageHostPath string
}
