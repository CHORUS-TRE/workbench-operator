package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// workspaceManifest returns a reader producing a Workspace YAML manifest for
// use with kubectl apply -f -.
func workspaceManifest(namespace, name string, airgapped bool, allowedFQDNs []string) io.Reader {
	fqdnJSON := "[]"
	if len(allowedFQDNs) > 0 {
		b, err := json.Marshal(allowedFQDNs)
		if err != nil {
			panic(fmt.Sprintf("failed to marshal allowedFQDNs: %v", err))
		}
		fqdnJSON = string(b)
	}

	manifest := fmt.Sprintf(`apiVersion: default.chorus-tre.ch/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  airgapped: %t
  allowedFQDNs: %s
`, name, namespace, airgapped, fqdnJSON)

	return strings.NewReader(manifest)
}
