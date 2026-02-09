package controller

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// fqdnPattern validates FQDN entries: optional leading wildcard (*.), then
// DNS labels separated by dots. Matches "example.com", "*.corp.internal", etc.
var fqdnPattern = regexp.MustCompile(`^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)*$`)

// validateFQDNs checks that every AllowedFQDNs entry is a plausible DNS name
// or wildcard pattern. Returns nil on success or an error describing the first
// invalid entry.
func validateFQDNs(entries []string) error {
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return fmt.Errorf("empty FQDN entry")
		}
		if !fqdnPattern.MatchString(trimmed) {
			return fmt.Errorf("invalid FQDN entry: %q", trimmed)
		}
	}
	return nil
}

// buildNetworkPolicy constructs a namespaced CiliumNetworkPolicy (unstructured)
// that enforces workspace-level egress policy (DNS + intra-namespace + optional
// FQDN allowlist or full internet).
//
// Policy mapping:
//   - Airgapped=true            → DNS + intra-namespace only
//   - Airgapped=false + FQDNs   → DNS + intra-namespace + FQDN allowlist
//   - Airgapped=false + no FQDNs → DNS + intra-namespace + full internet
func buildNetworkPolicy(workspace defaultv1alpha1.Workspace) *unstructured.Unstructured {
	labels := map[string]any{
		"workspace": workspace.Name,
	}

	egressRules := []map[string]any{
		// DNS to kube-system (kube-dns)
		{
			"toEndpoints": []map[string]any{
				{
					"matchLabels": map[string]any{
						"k8s:io.kubernetes.pod.namespace": "kube-system",
					},
				},
			},
			"toPorts": []map[string]any{
				{
					"ports": []map[string]any{
						{"port": "53", "protocol": "UDP"},
						{"port": "53", "protocol": "TCP"},
					},
					"rules": map[string]any{
						"dns": []map[string]any{
							{"matchPattern": "*"},
						},
					},
				},
			},
		},
		// Intra-namespace traffic (empty matchLabels selects all pods in the namespace)
		{
			"toEndpoints": []map[string]any{
				{
					"matchLabels": map[string]any{},
				},
			},
		},
	}

	if !workspace.Spec.Airgapped {
		fqdnSelectors := toFQDNSelectors(workspace.Spec.AllowedFQDNs)
		if len(fqdnSelectors) > 0 {
			egressRules = append(egressRules, map[string]any{
				"toFQDNs": fqdnSelectors,
				"toPorts": httpPortRules(),
			})
		} else {
			// No FQDNs specified and not airgapped → allow all internet
			egressRules = append(egressRules, map[string]any{
				"toCIDR": []string{"0.0.0.0/0", "::/0"},
			})
		}
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("%s-egress", workspace.Name),
				"namespace": workspace.Namespace,
				"labels":    labels,
			},
			"spec": map[string]any{
				// Empty matchLabels: policy applies to all pods in the namespace
				"endpointSelector": map[string]any{
					"matchLabels": map[string]any{},
				},
				"egress": egressRules,
			},
		},
	}
}

func httpPortRules() []map[string]any {
	return []map[string]any{
		{
			"ports": []map[string]any{
				{"port": "80", "protocol": "TCP"},
				{"port": "443", "protocol": "TCP"},
			},
			"rules": map[string]any{
				"http": []map[string]any{
					{},
				},
			},
		},
	}
}

func toFQDNSelectors(entries []string) []map[string]any {
	seen := map[string]struct{}{}
	var selectors []map[string]any

	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		if strings.Contains(trimmed, "*") {
			key := "pattern:" + trimmed
			if _, exists := seen[key]; exists {
				continue
			}
			selectors = append(selectors, map[string]any{"matchPattern": trimmed})
			seen[key] = struct{}{}
			continue
		}

		nameKey := "name:" + trimmed
		if _, exists := seen[nameKey]; !exists {
			selectors = append(selectors, map[string]any{"matchName": trimmed})
			seen[nameKey] = struct{}{}
		}

		pattern := fmt.Sprintf("*.%s", trimmed)
		patternKey := "pattern:" + pattern
		if _, exists := seen[patternKey]; !exists {
			selectors = append(selectors, map[string]any{"matchPattern": pattern})
			seen[patternKey] = struct{}{}
		}
	}

	return selectors
}
