package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
)

var _ = Describe("buildNetworkPolicy", func() {
	baseWorkbench := func() defaultv1alpha1.Workbench {
		return defaultv1alpha1.Workbench{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "wb",
				Namespace: "default",
			},
			Spec: defaultv1alpha1.WorkbenchSpec{
				Server: defaultv1alpha1.WorkbenchServer{},
			},
		}
	}

	It("builds DNS + intra-workbench egress by default", func() {
		wb := baseWorkbench()

		cnp := buildNetworkPolicy(wb)
		Expect(cnp).NotTo(BeNil())
		Expect(cnp.GetNamespace()).To(Equal("default"))

		spec := cnp.Object["spec"].(map[string]any)
		es := spec["endpointSelector"].(map[string]any)
		Expect(es["matchLabels"]).To(HaveKeyWithValue(matchingLabel, "wb"))

		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(2))

		dnsRule := egress[0]
		Expect(dnsRule["toEndpoints"]).NotTo(BeEmpty())
		Expect(dnsRule["toPorts"]).NotTo(BeEmpty())

		intraRule := egress[1]
		toEndpoints := intraRule["toEndpoints"].([]map[string]any)
		Expect(toEndpoints).To(HaveLen(1))
		Expect(toEndpoints[0]["matchLabels"]).To(HaveKeyWithValue(matchingLabel, "wb"))
	})

	It("adds FQDN allowlist rules with HTTP/HTTPS ports", func() {
		wb := baseWorkbench()
		wb.Spec.NetworkPolicy = &defaultv1alpha1.NetworkPolicySpec{
			AllowedTLDs: []string{"example.com", "*.corp.internal"},
		}

		cnp := buildNetworkPolicy(wb)
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

	It("allows full internet when enabled", func() {
		wb := baseWorkbench()
		wb.Spec.NetworkPolicy = &defaultv1alpha1.NetworkPolicySpec{
			AllowInternet: true,
		}

		cnp := buildNetworkPolicy(wb)
		spec := cnp.Object["spec"].(map[string]any)
		egress := spec["egress"].([]map[string]any)
		Expect(egress).To(HaveLen(3))

		allowInternetRule := egress[2]
		Expect(allowInternetRule["toCIDR"]).To(ContainElements("0.0.0.0/0", "::/0"))
	})
})
