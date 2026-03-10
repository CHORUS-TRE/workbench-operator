package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// workspaceWithServiceManifest returns a Workspace manifest that includes a postgres service entry.
// The chart registry and project are resolved from the operator's --registry and --services-repository flags.
func workspaceWithServiceManifest(namespace, name, networkPolicy, serviceState string) io.Reader {
	manifest := fmt.Sprintf(`apiVersion: default.chorus-tre.ch/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  networkPolicy: %s
  services:
    postgres:
      state: %s
      chart:
        repository: services/postgres
        tag: "1.6.1"
      credentials:
        secretName: postgres-creds
        paths:
          - settings.superuserPassword
      values:
        settings:
          superuser: postgres
          superuserDatabase: devdb
        storage:
          requestedSize: 1Gi
      connectionInfoTemplate: "postgresql://postgres@{{.ReleaseName}}.{{.Namespace}}:5432/devdb (secret: {{.SecretName}})"
`, name, namespace, networkPolicy, serviceState)

	return strings.NewReader(manifest)
}

// workspaceManifest returns a reader producing a Workspace YAML manifest for
// use with kubectl apply -f -.
func workspaceManifest(namespace, name, networkPolicy string, allowedFQDNs []string) io.Reader {
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
  networkPolicy: %s
  allowedFQDNs: %s
`, name, namespace, networkPolicy, fqdnJSON)

	return strings.NewReader(manifest)
}
