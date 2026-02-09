package controller

import (
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
})
