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

// ValidateFQDNs checks that every AllowedFQDNs entry is a plausible DNS name
// or wildcard pattern. Returns nil on success or an error describing the first
// invalid entry. Enforces RFC 1035 limits: max 63 chars per label, max 253 chars total.
func ValidateFQDNs(entries []string) error {
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

// InternalService describes a platform-internal service that workspace pods should always
// be able to reach, regardless of the workspace's network isolation mode.
// The FQDN must be validated against the cluster (Ingress host) before being added to the network policy.
type InternalService struct {
	// Namespace identifies the service's own Kubernetes namespace (e.g. "gitlab", "i2b2").
	// Documentation only — it does NOT scope HTTPRoute validation (routes are searched
	// cluster-wide since they are managed centrally in the gateway namespace) and does NOT
	// appear in the generated CiliumNetworkPolicy (egress targets are the namespaces in
	// NetworkPolicyNamespaces.AllowedEgress). What restricts workspace pods to specific
	// services is the serverNames SNI list in the CNP, derived from the FQDN field.
	Namespace string
	// FQDN is the fully-qualified domain name of the internal service (e.g. "gitlab.int.chorus-tre.ch").
	FQDN string
	// Ports is the list of TCP ports declared for this service (e.g. ["443", "22"]).
	// RESERVED — this field has NO effect on the generated CiliumNetworkPolicy.
	// buildNetworkPolicy always hardcodes port 443 in the egress rule.
	// Non-443 ports (e.g. SSH/22) require a separate egress path and are not yet implemented.
	Ports []string
}

// NetworkPolicyNamespaces holds the platform namespace names used in CNP rules.
// These are configurable to support different cluster topologies.
type NetworkPolicyNamespaces struct {
	// AllowedIngress is the list of namespaces whose pods may initiate connections INTO workspace pods
	// (e.g. backend, prometheus). No port restriction is applied.
	AllowedIngress []string
	// AllowedEgress is the list of namespaces that workspace pods may connect OUT TO
	// (e.g. ingress-nginx, reached post-DNAT when accessing internal services).
	AllowedEgress []string
}

// buildNetworkPolicy constructs a namespaced CiliumNetworkPolicy (unstructured)
// that enforces workspace-level egress and ingress policy.
//
// Egress policy mapping:
//   - Open          → kube-dns + intra-namespace (pod + service) + internal services + full internet
//   - FQDNAllowlist → kube-dns + intra-namespace (pod + service) + internal services + FQDN allowlist
//   - Airgapped     → kube-dns + intra-namespace (pod + service) + internal services only
//
// Internal services are always allowed regardless of mode. They are reached via the
// cluster's Ingress controller (post-DNAT destination). A single toEndpoints rule
// for ns.AllowedEgress namespaces is emitted (when internalServices is non-empty),
// with Cilium native serverNames SNI filtering restricting access to only the listed FQDNs.
//
// Ingress policy: pods from ns.AllowedIngress namespaces and the workspace namespace
// may connect inbound. This prevents pods in other workspaces from reaching services here.
//
// Note: toServices is required alongside toEndpoints because in clusters running
// kube-proxy alongside Cilium, iptables DNAT (ClusterIP → pod IP) happens after
// Cilium's eBPF policy check. toEndpoints matches pod IPs (post-DNAT) and covers
// direct pod-to-pod traffic; toServices matches the ClusterIP identity (pre-DNAT)
// and covers traffic routed through Kubernetes services.
//
// IMPORTANT: Expects workspace.Spec.AllowedFQDNs to be pre-validated via ValidateFQDNs.
// Returns an error if invalid FQDNs are detected.
func buildNetworkPolicy(workspace defaultv1alpha1.Workspace, internalServices []InternalService, ns NetworkPolicyNamespaces) (*unstructured.Unstructured, error) {
	// Defensive check: FQDNs must be validated before calling this function
	if err := ValidateFQDNs(workspace.Spec.AllowedFQDNs); err != nil {
		return nil, fmt.Errorf("buildNetworkPolicy called with invalid FQDNs: %w", err)
	}
	labels := map[string]any{
		"workspace": workspace.Name,
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
		// Note: namespaceSelector is intentionally omitted — Cilium does not support it
		// in k8sServiceSelector (unknown field, stripped on apply causing reconcile loops).
		// Scoping to the local namespace is implicit for namespaced CiliumNetworkPolicies.
		{
			"toServices": []map[string]any{
				{
					"k8sServiceSelector": map[string]any{
						"selector": map[string]any{"matchLabels": map[string]any{}},
					},
				},
			},
		},
	}

	// Internal platform services: always allowed in all modes (Open, Airgapped, FQDNAllowlist).
	// A single egress rule targets the Ingress controller namespace (post-DNAT destination)
	// with Cilium native serverNames SNI filtering restricting access to only the declared FQDNs.
	//
	// Why toEndpoints instead of toFQDNs: Cilium's eBPF performs service DNAT (LoadBalancer
	// IP → ingress-nginx pod IP) before the policy check. toFQDNs generates a CIDR for the
	// resolved IP but after DNAT the destination is a Cilium endpoint — CIDRs do not match
	// endpoints. toEndpoints matches the post-DNAT pod IP directly.
	//
	// Why serverNames: toEndpoints alone would allow access to ALL services behind the shared
	// ingress. The serverNames field triggers Cilium's native Envoy tls_inspector to extract
	// the SNI from the TLS ClientHello, restricting access to only the listed FQDNs and
	// preventing intra-CHORUS data exfiltration.
	//
	// Note: serverNames uses native Envoy TLS inspection, NOT the deprecated proxylib Go
	// extension (l7proto: tls). Proxylib was deprecated in Cilium v1.16 and is non-functional
	// in v1.18+ builds that ship without the Go parser (confirmed on v1.18.4: "Unknown parser").
	// It was fully removed in v1.20.
	//
	// Why Open mode (toCIDR: 0.0.0.0/0) does not bypass SNI: Cilium classifies pod IPs as
	// endpoints, not world. After service DNAT the destination is the ingress-nginx pod IP —
	// a Cilium endpoint — which is never matched by a CIDR rule. The toEndpoints+serverNames
	// rule therefore remains the only path to ingress-nginx regardless of the workspace's
	// network mode.
	if len(internalServices) > 0 && len(ns.AllowedEgress) > 0 {
		egressEndpoints := make([]map[string]any, 0, len(ns.AllowedEgress))
		for _, egressNS := range ns.AllowedEgress {
			egressEndpoints = append(egressEndpoints, map[string]any{
				"matchLabels": map[string]any{
					"k8s:io.kubernetes.pod.namespace": egressNS,
				},
			})
		}
		egressRules = append(egressRules, map[string]any{
			"toEndpoints": egressEndpoints,
			"toPorts": []map[string]any{
				{
					"ports": []map[string]any{
						{"port": "443", "protocol": "TCP"},
					},
					"serverNames": internalServiceFQDNs(internalServices),
				},
			},
		})
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

	// Ingress: allow connections from the workspace namespace itself (intra-workspace pod
	// traffic) and from all ns.AllowedIngress namespaces (e.g. backend, prometheus).
	// No toPorts restriction — workbench pods use arbitrary ports internally.
	ingressEndpoints := []map[string]any{
		{
			"matchLabels": map[string]any{
				"k8s:io.kubernetes.pod.namespace": workspace.Namespace,
			},
		},
	}
	for _, n := range ns.AllowedIngress {
		ingressEndpoints = append(ingressEndpoints, map[string]any{
			"matchLabels": map[string]any{
				"k8s:io.kubernetes.pod.namespace": n,
			},
		})
	}
	ingressRules := []map[string]any{
		{"fromEndpoints": ingressEndpoints},
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

// internalServiceFQDNs extracts the FQDN list from internal services for use
// as Cilium serverNames in CNP toPorts rules (native Envoy TLS SNI filtering).
// FQDNs are normalized (lowercased, trimmed), deduplicated, and empty entries are skipped.
func internalServiceFQDNs(services []InternalService) []string {
	seen := map[string]struct{}{}
	fqdns := make([]string, 0, len(services))
	for _, svc := range services {
		n := normalizeFQDNEntry(svc.FQDN)
		if n == "" {
			continue
		}
		if _, exists := seen[n]; exists {
			continue
		}
		seen[n] = struct{}{}
		fqdns = append(fqdns, n)
	}
	return fqdns
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
// Assumes entries have been pre-validated via ValidateFQDNs (no trimming needed).
// Deduplicates entries and generates:
//   - matchName for exact domains (e.g. "example.com")
//   - matchPattern for explicit wildcard domains only (e.g. "*.example.com")
func toFQDNSelectors(entries []string) []map[string]any {
	seen := map[string]struct{}{}
	var selectors []map[string]any

	for _, entry := range entries {
		// Canonicalize output. ValidateFQDNs() also normalizes for checking, but
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
