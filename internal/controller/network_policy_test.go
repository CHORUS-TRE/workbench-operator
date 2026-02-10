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
				Airgapped: true,
			},
		}
	}

	It("builds DNS + intra-namespace egress for airgapped workspace", func() {
		ws := baseWorkspace()

		cnp := buildNetworkPolicy(ws)
		Expect(cnp).NotTo(BeNil())
		Expect(cnp.GetName()).To(Equal("workspace-egress"))
		Expect(cnp.GetNamespace()).To(Equal("workspace-ns"))

		spec := cnp.Object["spec"].(map[string]any)
		es := spec["endpointSelector"].(map[string]any)
		Expect(es["matchLabels"]).To(BeEmpty())

		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(2))

		dnsRule := egress[0]
		Expect(dnsRule["toEndpoints"]).NotTo(BeEmpty())
		Expect(dnsRule["toPorts"]).NotTo(BeEmpty())

		intraRule := egress[1]
		toEndpoints := intraRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(1))
		Expect(toEndpoints[0]["matchLabels"]).To(BeEmpty())
	})

	It("adds FQDN allowlist rules with HTTP/HTTPS ports when not airgapped", func() {
		ws := baseWorkspace()
		ws.Spec.Airgapped = false
		ws.Spec.AllowedFQDNs = []string{"example.com", "*.corp.internal"}

		cnp := buildNetworkPolicy(ws)
		spec := cnp.Object["spec"].(map[string]any)
		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))

		fqdnRule := egress[2]
		toFQDNs := fqdnRule["toFQDNs"].([]map[string]any)
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchName", "example.com")))
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchPattern", "*.example.com")))
		Expect(toFQDNs).To(ContainElement(HaveKeyWithValue("matchPattern", "*.corp.internal")))

		toPorts := fqdnRule["toPorts"].([]map[string]any)
		Expect(toPorts).To(HaveLen(1))
		ports := toPorts[0]["ports"].([]map[string]any)
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "80")))
		Expect(ports).To(ContainElement(HaveKeyWithValue("port", "443")))
	})

	It("allows full internet when not airgapped and no FQDNs specified", func() {
		ws := baseWorkspace()
		ws.Spec.Airgapped = false

		cnp := buildNetworkPolicy(ws)
		spec := cnp.Object["spec"].(map[string]any)
		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))

		allowInternetRule := egress[2]
		Expect(allowInternetRule["toCIDR"]).To(ContainElements("0.0.0.0/0", "::/0"))
	})

	It("uses empty endpoint selector (all pods in namespace)", func() {
		ws := baseWorkspace()
		cnp := buildNetworkPolicy(ws)

		spec := cnp.Object["spec"].(map[string]any)
		es := spec["endpointSelector"].(map[string]any)
		ml := es["matchLabels"].(map[string]any)
		Expect(ml).To(BeEmpty())
	})
})

var _ = Describe("validateFQDNs", func() {
	It("accepts valid FQDNs", func() {
		err := validateFQDNs([]string{"example.com", "sub.example.com", "a.b.c.d.com"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts valid wildcard patterns", func() {
		err := validateFQDNs([]string{"*.example.com", "*.corp.internal"})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects empty entries", func() {
		err := validateFQDNs([]string{"example.com", "", "other.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("empty FQDN"))
	})

	It("rejects entries with spaces", func() {
		err := validateFQDNs([]string{"not a domain"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDN"))
	})

	It("rejects entries with invalid characters", func() {
		err := validateFQDNs([]string{"exam!ple.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("invalid FQDN"))
	})

	It("accepts an empty list", func() {
		err := validateFQDNs([]string{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("accepts nil", func() {
		err := validateFQDNs(nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects FQDN exceeding total length limit (253 chars)", func() {
		// Create a FQDN with 254 characters (exceeds max)
		longFQDN := strings.Repeat("a", 240) + ".example.com" // 240 + 1 + 11 + 1 + 3 = 256 chars
		err := validateFQDNs([]string{longFQDN})
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
		err := validateFQDNs([]string{fqdn253})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects FQDN with label exceeding 63 chars", func() {
		// Create a label with 64 characters (exceeds max)
		longLabel := strings.Repeat("x", 64)
		fqdn := longLabel + ".example.com"
		err := validateFQDNs([]string{fqdn})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})

	It("accepts FQDN with label at exactly 63 chars", func() {
		// Create a label with exactly 63 characters
		label63 := strings.Repeat("a", 63)
		fqdn := label63 + ".example.com"
		err := validateFQDNs([]string{fqdn})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects wildcard FQDN with label exceeding 63 chars", func() {
		// Wildcard should not count toward label length, but the domain after it should be validated
		longLabel := strings.Repeat("y", 64)
		fqdn := "*." + longLabel + ".example.com"
		err := validateFQDNs([]string{fqdn})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})

	It("accepts wildcard FQDN with valid label lengths", func() {
		label63 := strings.Repeat("a", 63)
		fqdn := "*." + label63 + ".com"
		err := validateFQDNs([]string{fqdn})
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects multiple FQDNs when one exceeds label length", func() {
		longLabel := strings.Repeat("z", 64)
		err := validateFQDNs([]string{"valid.example.com", longLabel + ".bad.com", "another.valid.com"})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("label exceeding maximum length of 63"))
	})
})
