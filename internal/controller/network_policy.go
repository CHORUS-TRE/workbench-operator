package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// fqdnPattern validates FQDN entries: optional leading wildcard (*.), then
// DNS labels separated by dots. Requires at least one dot (two labels).
// Matches "example.com", "*.corp.internal", etc. Does not match single-label
// names like "localhost".
var fqdnPattern = regexp.MustCompile(`^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?)+$`)

const (
	// RFC 1035: DNS labels must not exceed 63 octets
	maxDNSLabelLength = 63
	// RFC 1035: Full domain name must not exceed 253 octets
	maxFQDNLength = 253
	// Kubernetes object names are DNS subdomains (max 253 chars).
	maxK8sNameLength = 253
	// Truncated names use "-netpol-" + hash as a stable suffix.
	cnpLongNameSuffix = "-netpol-"
	cnpNameSuffix     = "-netpol"
	cnpNameHashLen    = 10
)

func normalizeFQDNEntry(entry string) string {
	// Canonicalize user input for policy generation and duplicate detection.
	// DNS names are case-insensitive; leading/trailing whitespace is unintentional.
	return strings.ToLower(strings.TrimSpace(entry))
}

func cnpNameForWorkspace(workspaceName string) string {
	name := workspaceName + cnpNameSuffix
	if len(name) <= maxK8sNameLength {
		return name
	}

	// Ensure name stays within DNS subdomain length. Use a stable hash of the workspace name
	// so the truncated name is deterministic and collision-resistant.
	sum := sha256.Sum256([]byte(workspaceName))
	hash := hex.EncodeToString(sum[:])[:cnpNameHashLen]
	suffix := cnpLongNameSuffix + hash

	maxPrefixLen := maxK8sNameLength - len(suffix)
	if maxPrefixLen < 1 {
		// Extremely defensive: fallback to something valid and stable.
		return "egress-" + hash
	}

	prefix := workspaceName
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
	}
	// DNS subdomains must end with an alphanumeric char; truncation may leave '-' or '.'.
	prefix = strings.TrimRight(prefix, "-.")
	if prefix == "" {
		prefix = "ws"
	}

	return prefix + suffix
}

// validateFQDNs checks that every AllowedFQDNs entry is a plausible DNS name
// or wildcard pattern. Returns nil on success or an error describing the first
// invalid entry. Enforces RFC 1035 limits: max 63 chars per label, max 253 chars total.
func validateFQDNs(entries []string) error {
	seen := map[string]struct{}{}
	for _, entry := range entries {
		normalized := normalizeFQDNEntry(entry)
		if normalized == "" {
			return fmt.Errorf("empty FQDN entry")
		}
		if _, exists := seen[normalized]; exists {
			return fmt.Errorf("duplicate FQDN entry (case-insensitive): %q", strings.TrimSpace(entry))
		}
		seen[normalized] = struct{}{}

		// Check total length (RFC 1035: max 253 octets)
		if len(normalized) > maxFQDNLength {
			return fmt.Errorf("FQDN entry exceeds maximum length of %d characters: %q (length: %d)", maxFQDNLength, strings.TrimSpace(entry), len(normalized))
		}

		if !fqdnPattern.MatchString(normalized) {
			return fmt.Errorf("invalid FQDN entry: %q", strings.TrimSpace(entry))
		}

		// Check individual label lengths (RFC 1035: max 63 octets per label)
		// Strip wildcard prefix if present for label validation
		fqdnToCheck := normalized
		if strings.HasPrefix(normalized, "*.") {
			fqdnToCheck = normalized[2:]
		}

		labels := strings.Split(fqdnToCheck, ".")
		for _, label := range labels {
			if len(label) > maxDNSLabelLength {
				return fmt.Errorf("FQDN entry %q contains label exceeding maximum length of %d characters: %q (length: %d)", strings.TrimSpace(entry), maxDNSLabelLength, label, len(label))
			}
		}
	}
	return nil
}

// buildNetworkPolicy constructs a namespaced CiliumNetworkPolicy (unstructured)
// that enforces workspace-level egress and ingress policy.
//
// Egress policy mapping:
//   - Open          → kube-dns + intra-namespace (pod + service) + full internet
//   - FQDNAllowlist → kube-dns + intra-namespace (pod + service) + FQDN allowlist
//   - Airgapped     → kube-dns + intra-namespace (pod + service) only
//
// Ingress policy: only pods within the same namespace may connect inbound.
// This prevents pods in other workspaces from reaching services in this workspace.
//
// Note: toServices is required alongside toEndpoints because in clusters running
// kube-proxy alongside Cilium, iptables DNAT (ClusterIP → pod IP) happens after
// Cilium's eBPF policy check. toEndpoints matches pod IPs (post-DNAT) and covers
// direct pod-to-pod traffic; toServices matches the ClusterIP identity (pre-DNAT)
// and covers traffic routed through Kubernetes services.
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

	// namespaceLabelSelector targets a Kubernetes Namespace object by its auto-assigned
	// metadata label. Used in toServices.k8sServiceSelector.namespaceSelector, which
	// selects namespace resources — not pods.
	//
	// toEndpoints/fromEndpoints rules use the Cilium-synthetic label
	// "k8s:io.kubernetes.pod.namespace" instead, which is stamped on pod identities
	// (not namespace objects) by Cilium's policy engine.
	namespaceLabelSelector := map[string]any{
		"matchLabels": map[string]any{
			"kubernetes.io/metadata.name": workspace.Namespace,
		},
	}

	// Note: no fromEndpoints/fromServices filter is needed on any rule below.
	// The top-level endpointSelector (empty matchLabels) already scopes this entire
	// policy to pods in the workspace namespace — Cilium only applies these egress
	// rules to pods selected by that selector.
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
		// Intra-namespace pod-to-pod traffic (direct pod IP, post-DNAT).
		{
			"toEndpoints": []map[string]any{
				{
					"matchLabels": map[string]any{
						"k8s:io.kubernetes.pod.namespace": workspace.Namespace,
					},
				},
			},
		},
		// Intra-namespace service traffic (ClusterIP, pre-DNAT).
		// Required when kube-proxy handles DNAT via iptables after Cilium's policy check.
		// The empty selector matches all services in the namespace. This is intentional:
		// each workspace owns its namespace exclusively — no external services
		// (monitoring, mesh proxies, etc.) are deployed here by design.
		{
			"toServices": []map[string]any{
				{
					"k8sServiceSelector": map[string]any{
						"selector":          map[string]any{"matchLabels": map[string]any{}},
						"namespaceSelector": namespaceLabelSelector,
					},
				},
			},
		},
	}

	switch workspace.Spec.NetworkPolicy {
	case defaultv1alpha1.NetworkPolicyOpen:
		// Allow all internet on HTTP/HTTPS.
		egressRules = append(egressRules, map[string]any{
			"toCIDR":  []string{"0.0.0.0/0", "::/0"},
			"toPorts": httpPortRules(),
		})
	case defaultv1alpha1.NetworkPolicyFQDNAllowlist:
		// Allow only the specified FQDNs on HTTP/HTTPS.
		egressRules = append(egressRules, map[string]any{
			"toFQDNs": toFQDNSelectors(workspace.Spec.AllowedFQDNs),
			"toPorts": httpPortRules(),
		})
	default:
		// Airgapped: no external traffic beyond kube-dns and intra-namespace.
	}

	// Ingress: allow connections from:
	//   - same namespace (intra-workspace: workbench pods, service pods)
	//   - backend namespace (reverse-proxy to Xpra server on port 8080)
	// No toPorts restriction — workbench pods use arbitrary ports internally.
	ingressRules := []map[string]any{
		{
			"fromEndpoints": []map[string]any{
				{
					"matchLabels": map[string]any{
						"k8s:io.kubernetes.pod.namespace": workspace.Namespace,
					},
				},
				{
					"matchLabels": map[string]any{
						"k8s:io.kubernetes.pod.namespace": "backend",
					},
				},
			},
		},
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]any{
				"name":      cnpNameForWorkspace(workspace.Name),
				"namespace": workspace.Namespace,
				"labels":    labels,
			},
			"spec": map[string]any{
				// Empty matchLabels: policy applies to all pods in the namespace
				"endpointSelector": map[string]any{
					"matchLabels": map[string]any{},
				},
				"egress":  egressRules,
				"ingress": ingressRules,
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
		},
	}
}

// toFQDNSelectors converts validated FQDN entries into Cilium toFQDNs selectors.
// Assumes entries have been pre-validated via validateFQDNs (no trimming needed).
// Deduplicates entries and generates:
//   - matchName for exact domains (e.g. "example.com")
//   - matchPattern for explicit wildcard domains only (e.g. "*.example.com")
func toFQDNSelectors(entries []string) []map[string]any {
	seen := map[string]struct{}{}
	var selectors []map[string]any

	for _, entry := range entries {
		// Canonicalize output. validateFQDNs() also normalizes for checking, but
		// does not mutate the original slice.
		entry = normalizeFQDNEntry(entry)

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
	}

	return selectors
}
