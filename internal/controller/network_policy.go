package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// buildNetworkPolicy constructs a namespaced CiliumNetworkPolicy (unstructured)
// that enforces per-workbench egress policy (DNS + intra-workbench + optional
// FQDN allowlist or full internet).
func buildNetworkPolicy(workbench defaultv1alpha1.Workbench) *unstructured.Unstructured {
	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	spec := workbench.Spec.NetworkPolicy
	allowInternet := false
	var allowedTLDs []string
	if spec != nil {
		allowInternet = spec.AllowInternet
		allowedTLDs = spec.AllowedTLDs
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
		// Intra-workbench traffic
		{
			"toEndpoints": []map[string]any{
				{
					"matchLabels": labels,
				},
			},
		},
	}

	fqdnSelectors := toFQDNSelectors(allowedTLDs)
	if len(fqdnSelectors) > 0 {
		egressRules = append(egressRules, map[string]any{
			"toFQDNs": fqdnSelectors,
			"toPorts": httpPortRules(),
		})
	}

	if allowInternet {
		egressRules = append(egressRules, map[string]any{
			"toCIDR": []string{"0.0.0.0/0", "::/0"},
		})
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]any{
				"name":      fmt.Sprintf("%s-egress", workbench.Name),
				"namespace": workbench.Namespace,
				"labels":    labels,
			},
			"spec": map[string]any{
				"endpointSelector": map[string]any{
					"matchLabels": labels,
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
