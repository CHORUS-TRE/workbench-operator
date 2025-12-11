package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	defaultv1alpha1 "github.com/CHORUS-TRE/workbench-operator/api/v1alpha1"
	"github.com/cilium/cilium/pkg/policy/api"
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
		Expect(cnp.Namespace).To(Equal("default"))
		Expect(cnp.Spec).NotTo(BeNil())
		Expect(cnp.Spec.EndpointSelector.MatchLabels).To(HaveKeyWithValue(matchingLabel, "wb"))

		Expect(cnp.Spec.Egress).To(HaveLen(2))

		dnsRule := cnp.Spec.Egress[0]
		Expect(dnsRule.ToEndpoints).NotTo(BeEmpty())
		Expect(dnsRule.ToPorts).To(HaveLen(1))

		intraRule := cnp.Spec.Egress[1]
		Expect(intraRule.ToEndpoints).To(HaveLen(1))
		Expect(intraRule.ToEndpoints[0].MatchLabels).To(HaveKeyWithValue(matchingLabel, "wb"))
	})

	It("adds FQDN allowlist rules with HTTP/HTTPS ports", func() {
		wb := baseWorkbench()
		wb.Spec.NetworkPolicy = &defaultv1alpha1.NetworkPolicySpec{
			AllowedTLDs: []string{"example.com", "*.corp.internal"},
		}

		cnp := buildNetworkPolicy(wb)
		Expect(cnp.Spec.Egress).To(HaveLen(3))

		fqdnRule := cnp.Spec.Egress[2]
		Expect(fqdnRule.ToFQDNs).To(ContainElement(HaveField("MatchName", "example.com")))
		Expect(fqdnRule.ToFQDNs).To(ContainElement(HaveField("MatchPattern", "*.example.com")))
		Expect(fqdnRule.ToFQDNs).To(ContainElement(HaveField("MatchPattern", "*.corp.internal")))

		Expect(fqdnRule.ToPorts).To(HaveLen(1))
		Expect(fqdnRule.ToPorts[0].Ports).To(ContainElements(
			HaveField("Port", "80"),
			HaveField("Port", "443"),
		))
	})

	It("allows full internet when enabled", func() {
		wb := baseWorkbench()
		wb.Spec.NetworkPolicy = &defaultv1alpha1.NetworkPolicySpec{
			AllowInternet: true,
		}

		cnp := buildNetworkPolicy(wb)
		Expect(cnp.Spec.Egress).To(HaveLen(3))

		allowInternetRule := cnp.Spec.Egress[2]
		Expect(allowInternetRule.ToCIDR).To(ContainElements(apiCIDR("0.0.0.0/0"), apiCIDR("::/0")))
	})
})

// apiCIDR is a helper to avoid string casting in tests.
func apiCIDR(value string) api.CIDR {
	return api.CIDR(value)
}
