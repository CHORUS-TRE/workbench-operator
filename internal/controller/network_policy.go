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

const (
	// RFC 1035: DNS labels must not exceed 63 octets
	maxDNSLabelLength = 63
	// RFC 1035: Full domain name must not exceed 253 octets
	maxFQDNLength = 253
)

// validateFQDNs checks that every AllowedFQDNs entry is a plausible DNS name
// or wildcard pattern. Returns nil on success or an error describing the first
// invalid entry. Enforces RFC 1035 limits: max 63 chars per label, max 253 chars total.
func validateFQDNs(entries []string) error {
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			return fmt.Errorf("empty FQDN entry")
		}

		// Check total length (RFC 1035: max 253 octets)
		if len(trimmed) > maxFQDNLength {
			return fmt.Errorf("FQDN entry exceeds maximum length of %d characters: %q (length: %d)", maxFQDNLength, trimmed, len(trimmed))
		}

		if !fqdnPattern.MatchString(trimmed) {
			return fmt.Errorf("invalid FQDN entry: %q", trimmed)
		}

		// Check individual label lengths (RFC 1035: max 63 octets per label)
		// Strip wildcard prefix if present for label validation
		fqdnToCheck := trimmed
		if strings.HasPrefix(trimmed, "*.") {
			fqdnToCheck = trimmed[2:]
		}

		labels := strings.Split(fqdnToCheck, ".")
		for _, label := range labels {
			if len(label) > maxDNSLabelLength {
				return fmt.Errorf("FQDN entry %q contains label exceeding maximum length of %d characters: %q (length: %d)", trimmed, maxDNSLabelLength, label, len(label))
			}
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
//
// IMPORTANT: Expects workspace.Spec.AllowedFQDNs to be pre-validated via validateFQDNs.
// Returns an error if invalid FQDNs are detected.
func buildNetworkPolicy(workspace defaultv1alpha1.Workspace) (*unstructured.Unstructured, error) {
	// Defensive check: FQDNs must be validated before calling this function
	if err := validateFQDNs(workspace.Spec.AllowedFQDNs); err != nil {
		return nil, fmt.Errorf("buildNetworkPolicy called with invalid FQDNs: %w", err)
	}
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
			// No FQDNs specified and not airgapped → allow all internet on HTTP/HTTPS
			egressRules = append(egressRules, map[string]any{
				"toCIDR":  []string{"0.0.0.0/0", "::/0"},
				"toPorts": httpPortRules(),
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
	}, nil
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

// toFQDNSelectors converts validated FQDN entries into Cilium toFQDNs selectors.
// Assumes entries have been pre-validated via validateFQDNs (no trimming needed).
// Deduplicates entries and generates both exact match and wildcard patterns.
func toFQDNSelectors(entries []string) []map[string]any {
	seen := map[string]struct{}{}
	var selectors []map[string]any

	for _, entry := range entries {
		// No trimming - entries are expected to be pre-validated
		if entry == "" {
			continue
		}

		if strings.Contains(entry, "*") {
			key := "pattern:" + entry
			if _, exists := seen[key]; exists {
				continue
			}
			selectors = append(selectors, map[string]any{"matchPattern": entry})
			seen[key] = struct{}{}
			continue
		}

		nameKey := "name:" + entry
		if _, exists := seen[nameKey]; !exists {
			selectors = append(selectors, map[string]any{"matchName": entry})
			seen[nameKey] = struct{}{}
		}

		pattern := fmt.Sprintf("*.%s", entry)
		patternKey := "pattern:" + pattern
		if _, exists := seen[patternKey]; !exists {
			selectors = append(selectors, map[string]any{"matchPattern": pattern})
			seen[patternKey] = struct{}{}
		}
	}

	return selectors
}
