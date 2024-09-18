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
			workbench := Workbench{}

			workbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "",
				},
			}

			warnings, err := workbench.ValidateCreate()

			Expect(warnings).To(BeNil())
			Expect(err).To(HaveOccurred())
		})

		It("Should admit if all required fields are provided", func() {
			workbench := Workbench{}

			workbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "my-app",
				},
			}

			warnings, err := workbench.ValidateCreate()

			Expect(warnings).To(BeNil())
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should deny deleting an app", func() {
			workbench := Workbench{}

			workbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "",
				},
			}

			newWorkbench := Workbench{}

			warnings, err := newWorkbench.ValidateUpdate(&workbench)

			Expect(warnings).To(BeNil())
			Expect(err).To(HaveOccurred())
		})

		It("Should deny replacing an app", func() {
			workbench := Workbench{}

			workbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "my-app",
				},
			}

			newWorkbench := Workbench{}

			newWorkbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "another-app",
				},
			}

			warnings, err := newWorkbench.ValidateUpdate(&workbench)

			Expect(warnings).To(BeNil())
			Expect(err).To(HaveOccurred())
		})

		It("Should allow adding an app", func() {
			workbench := Workbench{}

			newWorkbench := Workbench{}

			newWorkbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "my-app",
				},
			}

			warnings, err := newWorkbench.ValidateUpdate(&workbench)

			Expect(warnings).To(BeNil())
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should allow adding many apps", func() {
			workbench := Workbench{}

			newWorkbench := Workbench{}

			newWorkbench.Spec.Apps = []WorkbenchApp{
				{
					Name: "my-app",
				},
				{
					Name: "my-app",
				},
				{
					Name: "my-app",
				},
			}

			warnings, err := newWorkbench.ValidateUpdate(&workbench)

			Expect(warnings).To(BeNil())
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
