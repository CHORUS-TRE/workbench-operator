package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/CHORUS-TRE/workbench-operator/test/utils"
)

const namespace = "workbench-operator-system"

var _ = Describe("controller", Ordered, func() {
	BeforeAll(func() {
		By("installing prometheus operator")
		Expect(utils.InstallPrometheusOperator()).To(Succeed())

		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		By("uninstalling the Prometheus manager bundle")
		utils.UninstallPrometheusOperator()

		By("removing manager namespace")
		cmd := exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	Context("Operator", func() {
		It("should run successfully", func() {
			var controllerPodName string
			var err error

			// projectimage stores the name of the image used in the example
			projectimage := "example.com/workbench-operator:v0.0.1"

			By("building the manager(Operator) image")
			cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("loading the the manager(Operator) image on Kind")
			err = utils.LoadImageToKindClusterWithName(projectimage)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("installing CRDs")
			cmd = exec.Command("make", "install")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("installing the CiliumNetworkPolicy CRD (before deploy so the controller discovers it on first start)")
			cmd = exec.Command("kubectl", "apply", "-f", "config/crd/thirdparty/cilium.io_ciliumnetworkpolicies.yaml")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("deploying the controller-manager")
			cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func() error {
				// Get pod name

				cmd = exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				podNames := utils.GetNonEmptyLines(string(podOutput))
				if len(podNames) != 1 {
					return fmt.Errorf("expect 1 controller pods running, but got %d", len(podNames))
				}
				controllerPodName = podNames[0]
				ExpectWithOffset(2, controllerPodName).Should(ContainSubstring("controller-manager"))

				// Validate pod status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				status, err := utils.Run(cmd)
				ExpectWithOffset(2, err).NotTo(HaveOccurred())
				if string(status) != "Running" {
					return fmt.Errorf("controller pod in %s status", status)
				}
				return nil
			}
			EventuallyWithOffset(1, verifyControllerUp, time.Minute, time.Second).Should(Succeed())
		})
	})

	Context("Network policies", Ordered, func() {
		const testNS = "netpol-test"

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNS)
			_, _ = utils.Run(cmd)
		})

		AfterAll(func() {
			By("removing test namespace")
			cmd := exec.Command("kubectl", "delete", "ns", testNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		AfterEach(func() {
			// Clean up workspaces in test namespace
			cmd := exec.Command("kubectl", "delete", "workspaces", "--all", "-n", testNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			// Wait for CNPs to be garbage collected
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicies", "-n", testNS, "-o", "jsonpath={.items}")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return string(out) == "[]" || string(out) == ""
			}, 30*time.Second, time.Second).Should(BeTrue())
		})

		It("creates a CiliumNetworkPolicy for an airgapped workspace", func() {
			By("creating an airgapped Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "airgapped-ws", true, nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is created")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "airgapped-ws-egress", "-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 30*time.Second, time.Second).Should(Succeed())

			By("verifying CNP has 2 egress rules (DNS + intra-namespace)")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "airgapped-ws-egress",
				"-n", testNS, "-o", "jsonpath={.spec.egress}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var egress []map[string]any
			Expect(json.Unmarshal(out, &egress)).To(Succeed())
			Expect(egress).To(HaveLen(2))

			By("verifying NetworkPolicyReady condition is True")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "workspace", "airgapped-ws",
					"-n", testNS, "-o",
					`jsonpath={.status.conditions[?(@.type=="NetworkPolicyReady")].status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return ""
				}
				return string(out)
			}, 30*time.Second, time.Second).Should(Equal("True"))
		})

		It("creates a CiliumNetworkPolicy with FQDN rules for non-airgapped workspace", func() {
			By("creating a non-airgapped Workspace with FQDNs")
			fqdns := []string{"example.com", "*.corp.internal"}
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "fqdn-ws", false, fqdns)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is created with 3 egress rules")
			Eventually(func() int {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "fqdn-ws-egress",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return -1
				}
				var egress []map[string]any
				if json.Unmarshal(out, &egress) != nil {
					return -1
				}
				return len(egress)
			}, 30*time.Second, time.Second).Should(Equal(3))

			By("verifying the FQDN rule contains expected selectors")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "fqdn-ws-egress",
				"-n", testNS, "-o", "jsonpath={.spec.egress[2].toFQDNs}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring("example.com"))
			Expect(string(out)).To(ContainSubstring("*.corp.internal"))
		})

		It("creates a CiliumNetworkPolicy with full internet for non-airgapped workspace without FQDNs", func() {
			By("creating a non-airgapped Workspace without FQDNs")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "open-ws", false, nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP has toCIDR rule for internet access")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "open-ws-egress",
					"-n", testNS, "-o", "jsonpath={.spec.egress[2].toCIDR}")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return len(out) > 0
			}, 30*time.Second, time.Second).Should(BeTrue())
		})

		It("sets owner reference so CNP is garbage-collected with workspace", func() {
			By("creating a Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "owner-ws", true, nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for CNP creation")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-egress", "-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 30*time.Second, time.Second).Should(Succeed())

			By("verifying owner reference on CNP")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-egress",
				"-n", testNS, "-o", "jsonpath={.metadata.ownerReferences[0].kind}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal("Workspace"))

			By("deleting the Workspace")
			cmd = exec.Command("kubectl", "delete", "workspace", "owner-ws", "-n", testNS)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is garbage-collected")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-egress",
					"-n", testNS, "--ignore-not-found", "-o", "name")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return string(out) == ""
			}, 30*time.Second, time.Second).Should(BeTrue())
		})

		It("updates CNP when workspace spec changes", func() {
			By("creating an airgapped Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "update-ws", true, nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for airgapped CNP (2 egress rules)")
			Eventually(func() int {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "update-ws-egress",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return -1
				}
				var egress []map[string]any
				if json.Unmarshal(out, &egress) != nil {
					return -1
				}
				return len(egress)
			}, 30*time.Second, time.Second).Should(Equal(2))

			By("switching workspace to non-airgapped")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "update-ws", false, nil)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is updated to 3 egress rules")
			Eventually(func() int {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "update-ws-egress",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return -1
				}
				var egress []map[string]any
				if json.Unmarshal(out, &egress) != nil {
					return -1
				}
				return len(egress)
			}, 30*time.Second, time.Second).Should(Equal(3))
		})
	})
})
