package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Workbench Webhook", func() {

	Context("When creating Workbench under Defaulting Webhook", func() {
		It("Should fill in the default value if a required field is empty", func() {

			workbench := Workbench{}

			workbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "foo",
				},
			}

			workbench.Default()

			Expect(workbench.Spec.Server.Version).To(Equal("latest"))
			Expect(workbench.Spec.Apps[0].Version).To(Equal("latest"))
		})
	})

	Context("When creating Workbench under Validating Webhook", func() {
		It("Should deny if a required field is empty", func() {

			// TODO(user): Add your logic here

		})

		It("Should admit if all required fields are provided", func() {

			// TODO(user): Add your logic here

		})
	})

})
