package controller

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

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

		cnp, err := buildNetworkPolicy(ws, nil)
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
		Expect(dnsRule["toEndpoints"]).NotTo(BeEmpty())
		Expect(dnsRule["toPorts"]).NotTo(BeEmpty())

		intraEndpointRule := egress[1]
		toEndpoints := intraEndpointRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(1))
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue("k8s:io.kubernetes.pod.namespace", "workspace-ns"))

		intraServiceRule := egress[2]
		toServices := intraServiceRule["toServices"].([]map[string]any)
		Expect(toServices).To(HaveLen(1))
		svcSelector := toServices[0]["k8sServiceSelector"].(map[string]any)
		nsSel := svcSelector["namespaceSelector"].(map[string]any)
		Expect(nsSel["matchLabels"]).To(HaveKeyWithValue("kubernetes.io/metadata.name", "workspace-ns"))

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

		cnp, err := buildNetworkPolicy(ws, nil)
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

		cnp, err := buildNetworkPolicy(ws, nil)
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

	It("uses empty endpoint selector (all pods in namespace)", func() {
		ws := baseWorkspace()
		cnp, err := buildNetworkPolicy(ws, nil)
		Expect(err).NotTo(HaveOccurred())

		spec := cnp.Object["spec"].(map[string]any)
		es := spec["endpointSelector"].(map[string]any)
		ml := es["matchLabels"].(map[string]any)
		Expect(ml).To(BeEmpty())
	})

	It("returns an error when called with invalid FQDNs", func() {
		ws := baseWorkspace()
		ws.Spec.NetworkPolicy = defaultv1alpha1.NetworkPolicyFQDNAllowlist
		ws.Spec.AllowedFQDNs = []string{"invalid domain with spaces"}

		_, err := buildNetworkPolicy(ws, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDNs"))
	})

	It("truncates CNP name when workspace name is near Kubernetes length limit", func() {
		ws := baseWorkspace()
		ws.Name = strings.Repeat("a", 253)

		cnp, err := buildNetworkPolicy(ws, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(cnp.GetName()).To(Equal(cnpNameForWorkspace(ws.Name)))
		Expect(len(cnp.GetName())).To(BeNumerically("<=", 253))
		Expect(cnp.GetName()).To(ContainSubstring("-netpol-"))
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
		{Namespace: "gitlab", FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443", "22"}},
		{Namespace: "i2b2", FQDN: "i2b2.chorus-tre.ch", Ports: []string{"443"}},
	}

	It("emits internal service egress rules in Airgapped mode", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, internalSvcs)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base rules + 2 internal service rules
		Expect(egress).To(HaveLen(5))

		gitlabRule := egress[3]
		toFQDNs := gitlabRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "gitlab.chorus-tre.ch")))
		toPorts := gitlabRule["toPorts"].([]map[string]any)
		ports := toPorts[0]["ports"].([]map[string]any)
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "22")))

		i2b2Rule := egress[4]
		toFQDNs2 := i2b2Rule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs2).To(ContainElement(HaveKeyWithValue("matchName", "i2b2.chorus-tre.ch")))
	})

	It("emits internal service egress rules in Open mode", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyOpen)
		cnp, err := buildNetworkPolicy(ws, internalSvcs)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base + 2 internal + 1 open-internet = 6
		Expect(egress).To(HaveLen(6))

		gitlabRule := egress[3]
		toFQDNs := gitlabRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "gitlab.chorus-tre.ch")))
	})

	It("emits internal service egress rules in FQDNAllowlist mode", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyFQDNAllowlist)
		ws.Spec.AllowedFQDNs = []string{"example.com"}
		cnp, err := buildNetworkPolicy(ws, internalSvcs)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		// 3 base + 2 internal + 1 allowlist = 6
		Expect(egress).To(HaveLen(6))

		gitlabRule := egress[3]
		toFQDNs := gitlabRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "gitlab.chorus-tre.ch")))
	})

	It("emits no extra rules when internal services list is empty", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, []InternalService{})
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))
	})

	It("emits no extra rules when internal services list is nil", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, nil)
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))
	})

	It("uses the FQDN as-is (normalization happens at flag parse time)", func() {
		ws := baseWorkspace(defaultv1alpha1.NetworkPolicyAirgapped)
		cnp, err := buildNetworkPolicy(ws, []InternalService{
			{Namespace: "gitlab", FQDN: "gitlab.chorus-tre.ch", Ports: []string{"443"}},
		})
		Expect(err).NotTo(HaveOccurred())

		egress := cnp.Object["spec"].(map[string]any)["egress"].([]map[string]any)
		rule := egress[3]
		toFQDNs := rule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "gitlab.chorus-tre.ch")))
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

	It("accepts an empty list", func() {
		err := ValidateFQDNs([]string{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts nil", func() {
		err := ValidateFQDNs(nil)
		Expect(err).NotTo(HaveOccurred())
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

	It("deduplicates exact domains even without validation", func() {
		selectors := toFQDNSelectors([]string{"example.com", "example.com"})
		Expect(selectors).To(HaveLen(1))
		Expect(selectors).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
	})
})

var _ = Describe("normalizeFQDNEntry", func() {
	It("lowercases uppercase input", func() {
		Expect(normalizeFQDNEntry("EXAMPLE.COM")).To(Equal("example.com"))
	})

	It("trims leading and trailing whitespace", func() {
		Expect(normalizeFQDNEntry("  example.com  ")).To(Equal("example.com"))
	})

	It("lowercases and trims simultaneously", func() {
		Expect(normalizeFQDNEntry("  ExAmPlE.CoM  ")).To(Equal("example.com"))
	})

	It("returns empty string for whitespace-only input", func() {
		Expect(normalizeFQDNEntry("   ")).To(Equal(""))
	})

	It("returns empty string for empty input", func() {
		Expect(normalizeFQDNEntry("")).To(Equal(""))
	})

	It("preserves valid wildcard prefix", func() {
		Expect(normalizeFQDNEntry("*.Corp.Internal")).To(Equal("*.corp.internal"))
	})
})

var _ = Describe("validateFQDNs (whitespace edge cases)", func() {
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

	It("falls back to 'ws' prefix when truncated name consists only of dashes", func() {
		// A workspace name made of dashes: truncation leaves only dashes, TrimRight
		// removes them all, so the prefix collapses to "" and falls back to "ws".
		allDashes := strings.Repeat("-", 300)
		name := cnpNameForWorkspace(allDashes)
		Expect(len(name)).To(BeNumerically("<=", 253))
		Expect(name).To(HavePrefix("ws"))
	})
})
