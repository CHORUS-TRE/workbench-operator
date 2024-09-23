package controller

// Config holds the global configuration that was given to the controller.
type Config struct {
	// Registry contains the hostname of the server and apps OCI images.
	Registry string
	// SocatImage is the image (with version) to expose X11 on a TCP socket.
	SocatImage string
	// XpraServerImage is the image (no version) used as the server.
	XpraServerImage string
}
