package controller

import (
	"fmt"
	"strings"

	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	slimv1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	"github.com/cilium/cilium/pkg/policy/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// buildNetworkPolicy constructs a namespaced CiliumNetworkPolicy that enforces
// per-workbench egress policy (DNS + intra-workbench + optional FQDN allowlist
// or full internet).
func buildNetworkPolicy(workbench defaultv1alpha1.Workbench) *ciliumv2.CiliumNetworkPolicy {
	labels := map[string]string{
		matchingLabel: workbench.Name,
	}

	endpointSelector := api.EndpointSelector{
		LabelSelector: &slimv1.LabelSelector{
			MatchLabels: labels,
		},
	}

	dnsRule := api.EgressRule{
		ToEndpoints: []api.EndpointSelector{
			{
				LabelSelector: &slimv1.LabelSelector{
					MatchLabels: map[string]string{
						// Allow kube-dns in kube-system for DNS resolution
						"k8s:io.kubernetes.pod.namespace": "kube-system",
					},
				},
			},
		},
		ToPorts: []api.PortRule{
			{
				Ports: []api.PortProtocol{
					{Port: "53", Protocol: api.ProtoUDP},
					{Port: "53", Protocol: api.ProtoTCP},
				},
				Rules: &api.L7Rules{
					DNS: []api.PortRuleDNS{
						{MatchPattern: "*"},
					},
				},
			},
		},
	}

	intraWorkbenchRule := api.EgressRule{
		ToEndpoints: []api.EndpointSelector{
			endpointSelector,
		},
	}

	spec := workbench.Spec.NetworkPolicy
	allowInternet := false
	var allowedTLDs []string
	if spec != nil {
		allowInternet = spec.AllowInternet
		allowedTLDs = spec.AllowedTLDs
	}

	egressRules := []api.EgressRule{
		dnsRule,
		intraWorkbenchRule,
	}

	fqdnSelectors := toFQDNSelectors(allowedTLDs)
	if len(fqdnSelectors) > 0 {
		egressRules = append(egressRules, api.EgressRule{
			ToFQDNs: fqdnSelectors,
			ToPorts: httpPortRules(),
		})
	}

	if allowInternet {
		// Allow full internet egress when explicitly requested.
		egressRules = append(egressRules, api.EgressRule{
			ToCIDR: api.CIDRSlice{
				"0.0.0.0/0",
				"::/0",
			},
		})
	}

	return &ciliumv2.CiliumNetworkPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "cilium.io/v2",
			Kind:       "CiliumNetworkPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-egress", workbench.Name),
			Namespace: workbench.Namespace,
			Labels:    labels,
		},
		Spec: &api.Rule{
			EndpointSelector: endpointSelector,
			Egress:           egressRules,
		},
	}
}

func httpPortRules() []api.PortRule {
	return []api.PortRule{
		{
			Ports: []api.PortProtocol{
				{Port: "80", Protocol: api.ProtoTCP},
				{Port: "443", Protocol: api.ProtoTCP},
			},
			Rules: &api.L7Rules{
				HTTP: []api.PortRuleHTTP{{}},
			},
		},
	}
}

func toFQDNSelectors(entries []string) []api.FQDNSelector {
	seen := map[string]struct{}{}
	var selectors []api.FQDNSelector

	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		// Preserve explicit wildcard patterns, otherwise allow both the base
		// domain and all subdomains.
		if strings.Contains(trimmed, "*") {
			if _, exists := seen["pattern:"+trimmed]; exists {
				continue
			}
			selectors = append(selectors, api.FQDNSelector{MatchPattern: trimmed})
			seen["pattern:"+trimmed] = struct{}{}
			continue
		}

		if _, exists := seen["name:"+trimmed]; !exists {
			selectors = append(selectors, api.FQDNSelector{MatchName: trimmed})
			seen["name:"+trimmed] = struct{}{}
		}

		pattern := fmt.Sprintf("*.%s", trimmed)
		if _, exists := seen["pattern:"+pattern]; !exists {
			selectors = append(selectors, api.FQDNSelector{MatchPattern: pattern})
			seen["pattern:"+pattern] = struct{}{}
		}
	}

	return selectors
}
