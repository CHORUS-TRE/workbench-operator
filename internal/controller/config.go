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
	// ImagePullSecrets holds the list of secret for all registries.
	ImagePullSecrets []string
}
