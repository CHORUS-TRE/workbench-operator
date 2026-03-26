package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

// netpolTestNS is the shared NetworkPolicyNamespaces fixture for network_policy tests.
var netpolTestNS = NetworkPolicyNamespaces{
	AllowedIngress: []string{"backend", "prometheus"},
	AllowedEgress:  []string{"ingress-nginx"},
}

var _ = Describe("buildNetworkPolicy", func() {
	baseWorkspace := func() defaultv1alpha1.Workspace {
		return defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workspace",
				Namespace: "workspace-ns",
			},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: defaultv1alpha1.NetworkPolicyAirgapped,
			},
		}
	}

	It("builds kube-dns + intra-namespace egress and intra-namespace ingress for Airgapped workspace", func() {
		ws := baseWorkspace()

		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(cnp).NotTo(BeNil())
		Expect(cnp.GetName()).To(Equal("workspace-netpol"))
		Expect(cnp.GetNamespace()).To(Equal("workspace-ns"))

		spec := cnp.Object["spec"].(map[string]any)
		es := spec["endpointSelector"].(map[string]any)
		Expect(es["matchLabels"]).To(BeEmpty())

		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))

		dnsRule := egress[0]
		dnsEndpoints := dnsRule["toEndpoints"].([]map[string]any)
		Expect(dnsEndpoints).To(HaveLen(1))
		Expect(dnsEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "kube-system"))
		dnsPorts := dnsRule["toPorts"].([]map[string]any)
		Expect(dnsPorts).To(HaveLen(1))
		dnsPorts0 := dnsPorts[0]["ports"].([]map[string]any)
		Expect(dnsPorts0).To(ConsistOf(
			And(HaveKeyWithValue("port", "53"), HaveKeyWithValue("protocol", "UDP")),
			And(HaveKeyWithValue("port", "53"), HaveKeyWithValue("protocol", "TCP")),
		))
		dnsRules := dnsPorts[0]["rules"].(map[string]any)
		Expect(dnsRules["dns"]).To(ContainElement(HaveKeyWithValue("matchPattern", "*")))

		intraEndpointRule := egress[1]
		toEndpoints := intraEndpointRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(1))
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "workspace-ns"))

		intraServiceRule := egress[2]
		toServices := intraServiceRule["toServices"].([]map[string]any)
		Expect(toServices).To(HaveLen(1))
		svcSelector := toServices[0]["k8sServiceSelector"].(map[string]any)
		Expect(svcSelector).To(HaveKey("selector"))
		Expect(svcSelector).NotTo(HaveKey("namespaceSelector"))

		ingress := spec["ingress"].([]map[string]any)
		Expect(ingress).To(HaveLen(1))
		fromEndpoints := ingress[0]["fromEndpoints"].([]map[string]any)
		Expect(fromEndpoints).To(HaveLen(3))
		Expect(fromEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "workspace-ns"))
		Expect(fromEndpoints[1]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "backend"))
		Expect(fromEndpoints[2]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "prometheus"))
	})

	It("adds FQDN allowlist rules with HTTP/HTTPS ports when FQDNAllowlist", func() {
		ws := baseWorkspace()
		ws.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyFQDNAllowlist
		ws.Spec.AllowedFQDNs = []string{"example.com", "*.corp.internal"}

		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		spec := cnp.Object["spec"].(map[string]any)
		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(4))

		fqdnRule := egress[3]
		toFQDNs := fqdnRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchPattern", "*.corp.internal")))

		toPorts := fqdnRule["toPorts"].([]map[string]any)
		Expect(toPorts).To(HaveLen(1))
		ports := toPorts[0]["ports"].([]map[string]any)
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "80")))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))
	})

	It("allows full internet on HTTP/HTTPS when Open and no FQDNs specified", func() {
		ws := baseWorkspace()
		ws.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyOpen

		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		spec := cnp.Object["spec"].(map[string]any)
		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(4))

		allowInternetRule := egress[3]
		Expect(allowInternetRule["toCIDR"]).To(ContainElements("0.0.0.0/0", "::/0"))

		toPorts := allowInternetRule["toPorts"].([]map[string]any)
		Expect(toPorts).To(HaveLen(1))
		ports := toPorts[0]["ports"].([]map[string]any)
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "80")))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))
	})

	It("emits a toFQDNs rule with nil selectors when FQDNAllowlist has an empty AllowedFQDNs list", func() {
		// ValidateFQDNs accepts an empty list, so buildNetworkPolicy must handle it.
		// toFQDNSelectors([]) returns nil → the egress rule is emitted with toFQDNs: nil.
		// The operator should not panic or error on this degenerate input.
		ws := baseWorkspace()
		ws.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyFQDNAllowlist
		ws.Spec.AllowedFQDNs = []string{}

		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base + 1 FQDN rule (even though toFQDNs is nil)
		Expect(egress).To(HaveLen(4))
		fqdnRule := egress[3]
		Expect(fqdnRule).To(HaveKey("toFQDNs"))
		Expect(fqdnRule["toFQDNs"]).To(BeNil())
	})

	It("returns an error when called with invalid FQDNs", func() {
		ws := baseWorkspace()
		ws.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyFQDNAllowlist
		ws.Spec.AllowedFQDNs = []string{"invalid domain with spaces"}

		_, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDNs"))
	})

	It("truncates CNP name when workspace name is near Kubernetes length limit", func() {
		ws := baseWorkspace()
		ws.Name = strings.Repeat("a", 253)

		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		Expect(cnp.GetName()).To(Equal(cnpNameForWorkspace(ws.Name)))
		Expect(len(cnp.GetName())).To(BeNumerically("<=", 253))
		Expect(cnp.GetName()).To(ContainSubstring("-netpol-"))
	})

	It("produces only workspace namespace in fromEndpoints when AllowedIngress is empty", func() {
		ws := baseWorkspace()
		emptyIngressNS := NetworkPolicyNamespaces{
			AllowedIngress: []string{},
			AllowedEgress:  []string{"ingress-nginx"},
		}
		cnp, err := buildNetworkPolicy(ws, nil, emptyIngressNS)
		Expect(err).NotTo(HaveOccurred())

		spec := cnp.Object["spec"].(map[string]any)
		ingress := spec["ingress"].([]map[string]any)
		fromEndpoints := ingress[0]["fromEndpoints"].([]map[string]any)
		Expect(fromEndpoints).To(HaveLen(1))
		Expect(fromEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "workspace-ns"))
	})

	It("uses AllowedIngress namespaces from ns in fromEndpoints", func() {
		ws := baseWorkspace()
		customNS := NetworkPolicyNamespaces{
			AllowedIngress: []string{"my-backend", "my-metrics"},
			AllowedEgress:  []string{"ingress-nginx"},
		}
		cnp, err := buildNetworkPolicy(ws, nil, customNS)
		Expect(err).NotTo(HaveOccurred())

		spec := cnp.Object["spec"].(map[string]any)
		ingress := spec["ingress"].([]map[string]any)
		fromEndpoints := ingress[0]["fromEndpoints"].([]map[string]any)
		Expect(fromEndpoints).To(HaveLen(3)) // workspace-ns + 2 custom
		Expect(fromEndpoints[1]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "my-backend"))
		Expect(fromEndpoints[2]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "my-metrics"))
	})
})

var _ = Describe("buildNetworkPolicy with internal services", func() {
	baseWorkspace := func(policy string) defaultv1alpha1.Workspace {
		return defaultv1alpha1.Workspace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "workspace",
				Namespace: "workspace-ns",
			},
			Spec: defaultv1alpha1.WorkspaceSpec{
				NetworkPolicy: policy,
			},
		}
	}

	internalSvcs := []InternalService{
		{Namespace: "gitlab", FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		{Namespace: "i2b2", FQDN: "i2b2.chorus-tre.ch", Ports: []string{"443"}},
	}

	It("emits a single ingress-nginx toEndpoints rule with SNI selectors in Airgapped mode", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, internalSvcs, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base rules + 1 ingress-nginx rule (replaces per-service toFQDNs)
		Expect(egress).To(HaveLen(4))

		ingressNginxRule := egress[3]
		toEndpoints := ingressNginxRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(1))
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "ingress-nginx"))

		toPorts := ingressNginxRule["toPorts"].([]map[string]any)
		Expect(toPorts).To(HaveLen(1))
		ports := toPorts[0]["ports"].([]map[string]any)
		Expect(ports).To(HaveLen(1))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))

		Expect(toPorts[0]).To(HaveKey("serverNames"))
		serverNames := toPorts[0]["serverNames"].([]string)
		Expect(serverNames).To(ContainElement("gitlab.chorus-tre.ch"))
		Expect(serverNames).To(ContainElement("i2b2.chorus-tre.ch"))
	})

	It("emits internal service rule in Open mode (before internet rule)", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyOpen)
		cnp, err := buildNetworkPolicy(ws, internalSvcs, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base + 1 ingress-nginx + 1 open-internet = 5
		Expect(egress).To(HaveLen(5))

		ingressNginxRule := egress[3]
		toEndpoints := ingressNginxRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "ingress-nginx"))

		// Verify the internet rule at [4] is still correct
		internetRule := egress[4]
		Expect(internetRule["toCIDR"]).To(ContainElements("0.0.0.0/0", "::/0"))
		ports := internetRule["toPorts"].([]map[string]any)[0]["ports"].([]map[string]any)
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "80")))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))
	})

	It("emits internal service rule in FQDNAllowlist mode (before allowlist rule)", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyFQDNAllowlist)
		ws.Spec.AllowedFQDNs = []string{"example.com"}
		cnp, err := buildNetworkPolicy(ws, internalSvcs, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base + 1 ingress-nginx + 1 allowlist = 5
		Expect(egress).To(HaveLen(5))

		ingressNginxRule := egress[3]
		toEndpoints := ingressNginxRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "ingress-nginx"))

		// Verify the FQDN allowlist rule at [4] is still correct
		allowlistRule := egress[4]
		toFQDNs := allowlistRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
	})

	It("emits no extra rules when internal services list is nil", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, nil, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))
	})

	It("emits no extra rules when internal services list is empty", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, []InternalService{}, netpolTestNS)
		Expect(err).NotTo(HaveOccurred())
		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))
	})

	It("emits toEndpoints for each AllowedEgress namespace when multiple are configured", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		multiEgressNS := NetworkPolicyNamespaces{
			AllowedIngress: []string{"backend"},
			AllowedEgress:  []string{"ingress-nginx", "ingress-nginx-internal"},
		}
		cnp, err := buildNetworkPolicy(ws, internalSvcs, multiEgressNS)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(4)) // 3 base + 1 ingress-nginx rule

		ingressNginxRule := egress[3]
		toEndpoints := ingressNginxRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(2))
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "ingress-nginx"))
		Expect(toEndpoints[1]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "ingress-nginx-internal"))
	})

	It("emits no extra rules when AllowedEgress is empty even with internal services", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		nsNoEgress := NetworkPolicyNamespaces{
			AllowedIngress: []string{"backend", "prometheus"},
			AllowedEgress:  []string{},
		}
		cnp, err := buildNetworkPolicy(ws, internalSvcs, nsNoEgress)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))
	})

})

var _ = Describe("internalServiceFQDNs", func() {
	It("normalizes uppercase FQDNs to lowercase", func() {
		svcs := []InternalService{
			{Namespace: "gitlab", FQDN: "GITLAB.CHORUS-TRE.CH", Ports: []string{"443"}},
		}
		Expect(internalServiceFQDNs(svcs)).To(ConsistOf("gitlab.chorus-tre.ch"))
	})

	It("trims whitespace from FQDNs", func() {
		svcs := []InternalService{
			{Namespace: "gitlab", FQDN: "  gitlab.chorus-tre.ch  ", Ports: []string{"443"}},
		}
		Expect(internalServiceFQDNs(svcs)).To(ConsistOf("gitlab.chorus-tre.ch"))
	})

	It("skips empty FQDNs", func() {
		svcs := []InternalService{
			{Namespace: "gitlab", FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
			{Namespace: "empty", FQDN: "", Ports: []string{"443"}},
			{Namespace: "spaces", FQDN: "   ", Ports: []string{"443"}},
		}
		Expect(internalServiceFQDNs(svcs)).To(ConsistOf("gitlab.chorus-tre.ch"))
	})

	It("deduplicates FQDNs (case-insensitive)", func() {
		svcs := []InternalService{
			{Namespace: "gitlab", FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
			{Namespace: "gitlab-mirror", FQDN: "GITLAB.CHORUS-TRE.CH", Ports: []string{"443"}},
		}
		Expect(internalServiceFQDNs(svcs)).To(ConsistOf("gitlab.chorus-tre.ch"))
	})

	It("returns empty slice for nil input", func() {
		Expect(internalServiceFQDNs(nil)).To(BeEmpty())
	})
})

var _ = Describe("ValidateFQDNs", func() {
	It("accepts valid FQDNs", func() {
		err := ValidateFQDNs([]string{"example.com", "sub.example.com", "a.b.c.d.com"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts valid wildcard patterns", func() {
		err := ValidateFQDNs([]string{"*.example.com", "*.corp.internal"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects empty entries", func() {
		err := ValidateFQDNs([]string{"example.com", "", "other.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty FQDN"))
	})

	It("rejects entries with spaces", func() {
		err := ValidateFQDNs([]string{"not a domain"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDN"))
	})

	It("rejects entries with invalid characters", func() {
		err := ValidateFQDNs([]string{"exam!ple.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDN"))
	})

	It("rejects single-label names (must contain at least one dot)", func() {
		err := ValidateFQDNs([]string{"localhost"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDN"))
	})

	It("accepts an empty list or nil", func() {
		Expect(ValidateFQDNs([]string{})).NotTo(HaveOccurred())
		Expect(ValidateFQDNs(nil)).NotTo(HaveOccurred())
	})

	It("rejects FQDN exceeding total length limit (253 chars)", func() {
		// Build a FQDN with valid labels (each ≤63) but total length > 253
		// 63 + 1 + 63 + 1 + 63 + 1 + 63 = 255 chars
		longFQDN := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63)
		Expect(len(longFQDN)).To(Equal(255))
		err := ValidateFQDNs([]string{longFQDN})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("exceeds maximum length of 253"))
	})

	It("accepts FQDN at exactly 253 chars", func() {
		// Create a FQDN with exactly 253 characters
		// Use labels of 63 chars each: 63 + 1 + 63 + 1 + 63 + 1 + 61 = 253
		label63 := strings.Repeat("a", 63)
		label61 := strings.Repeat("b", 61)
		fqdn253 := label63 + "." + label63 + "." + label63 + "." + label61
		Expect(len(fqdn253)).To(Equal(253))
		err := ValidateFQDNs([]string{fqdn253})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects FQDN with label exceeding 63 chars", func() {
		// Create a label with 64 characters (exceeds max)
		longLabel := strings.Repeat("x", 64)
		fqdn := longLabel + ".example.com"
		err := ValidateFQDNs([]string{fqdn})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})

	It("accepts FQDN with label at exactly 63 chars", func() {
		// Create a label with exactly 63 characters
		label63 := strings.Repeat("a", 63)
		fqdn := label63 + ".example.com"
		err := ValidateFQDNs([]string{fqdn})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects wildcard FQDN with label exceeding 63 chars", func() {
		// Wildcard should not count toward label length, but the domain after it should be validated
		longLabel := strings.Repeat("y", 64)
		fqdn := "*." + longLabel + ".example.com"
		err := ValidateFQDNs([]string{fqdn})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})

	It("accepts wildcard FQDN with valid label lengths", func() {
		label63 := strings.Repeat("a", 63)
		fqdn := "*." + label63 + ".com"
		err := ValidateFQDNs([]string{fqdn})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects case-insensitive duplicate FQDNs", func() {
		err := ValidateFQDNs([]string{"example.com", "EXAMPLE.COM"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate FQDN entry"))
	})

	It("rejects multiple FQDNs when one exceeds label length", func() {
		longLabel := strings.Repeat("z", 64)
		err := ValidateFQDNs([]string{"valid.example.com", longLabel + ".bad.com", "another.valid.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})

	It("accepts FQDN with surrounding whitespace as valid after trimming", func() {
		err := ValidateFQDNs([]string{"  example.com  "})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects whitespace-only entry (normalizes to empty)", func() {
		err := ValidateFQDNs([]string{"   "})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty FQDN"))
	})

	It("detects duplicates across mixed-case and whitespace variants", func() {
		err := ValidateFQDNs([]string{" EXAMPLE.COM ", "example.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("duplicate FQDN entry"))
	})
})

var _ = Describe("toFQDNSelectors", func() {
	It("generates matchName only for exact domains (no implicit wildcard)", func() {
		selectors := toFQDNSelectors([]string{"example.com"})
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(selectors).NotTo(ContainElement(HaveKeyWithValue("matchPattern", "*.example.com")))
	})

	It("normalizes domains to lowercase and trims whitespace", func() {
		selectors := toFQDNSelectors([]string{" ExAmPlE.CoM ", " *.CoRp.InTeRnAl "})
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchPattern", "*.corp.internal")))
	})

	It("generates matchPattern only for explicit wildcard domains", func() {
		selectors := toFQDNSelectors([]string{"*.example.com"})
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchPattern", "*.example.com")))
		Expect(selectors).NotTo(ContainElement(HaveKey("matchName")))
	})

	It("includes both when user explicitly opts into wildcard", func() {
		selectors := toFQDNSelectors([]string{"example.com", "*.example.com"})
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchPattern", "*.example.com")))
	})

	It("deduplicates entries", func() {
		selectors := toFQDNSelectors([]string{"example.com", "example.com", "*.example.com", "*.example.com"})
		// One matchName + one matchPattern
		Expect(selectors).To(HaveLen(2))
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchPattern", "*.example.com")))
	})

})

var _ = Describe("cnpNameForWorkspace", func() {
	It("returns short name with suffix when name fits within 253 chars", func() {
		name := cnpNameForWorkspace("my-workspace")
		Expect(name).To(Equal("my-workspace-netpol"))
		Expect(len(name)).To(BeNumerically("<=", 253))
	})

	It("hashes and truncates very long names", func() {
		long := strings.Repeat("a", 300)
		name := cnpNameForWorkspace(long)
		Expect(len(name)).To(BeNumerically("<=", 253))
		Expect(name).To(ContainSubstring("-netpol-"))
	})

	It("falls back to 'ws' prefix when truncation leaves only dashes", func() {
		// A name of 247+ dashes triggers the long path; after truncating to maxPrefixLen
		// and TrimRight("-"), the prefix is empty — must fall back to "ws".
		allDashes := strings.Repeat("-", 247)
		name := cnpNameForWorkspace(allDashes)
		Expect(name).To(HavePrefix("ws-netpol-"))
		Expect(len(name)).To(BeNumerically("<=", 253))
	})

	It("uses short form at the exact boundary (246 chars fits, 247 does not)", func() {
		// maxK8sNameLength=253, cnpNameSuffix="-netpol" (7 chars) → threshold is 246.
		// 246-char name: 246+7=253 → fits, short form.
		atBoundary := strings.Repeat("a", 246)
		name := cnpNameForWorkspace(atBoundary)
		Expect(name).To(Equal(atBoundary + "-netpol"))
		Expect(len(name)).To(Equal(253))

		// 247-char name: 247+7=254 → exceeds limit, must truncate with hash.
		overBoundary := strings.Repeat("a", 247)
		name = cnpNameForWorkspace(overBoundary)
		Expect(len(name)).To(BeNumerically("<=", 253))
		Expect(name).To(ContainSubstring("-netpol-"))
	})

})
