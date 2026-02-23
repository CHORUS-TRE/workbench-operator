package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/CHORUS-TRE/workbench-operator/test/utils"
)

const namespace = "workbench-operator-system"

// dumpDiagnostics outputs controller logs, workspace details, and events
// for the given namespace to help debug test failures.
func dumpDiagnostics(ns string) {
	fmt.Fprintf(GinkgoWriter, "\n=== DIAGNOSTIC DUMP (namespace: %s) ===\n", ns)

	// Controller logs
	fmt.Fprintln(GinkgoWriter, "\n--- Controller logs ---")
	cmd := exec.Command("kubectl", "logs",
		"deployment/workbench-operator-controller-manager",
		"-n", namespace, "--tail=80")
	out, err := utils.Run(cmd)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "failed to get controller logs: %v\n", err)
	} else {
		fmt.Fprintln(GinkgoWriter, string(out))
	}

	// Workspace details
	fmt.Fprintln(GinkgoWriter, "\n--- Workspaces ---")
	cmd = exec.Command("kubectl", "get", "workspaces", "-n", ns, "-o", "yaml")
	out, err = utils.Run(cmd)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "failed to get workspaces: %v\n", err)
	} else {
		fmt.Fprintln(GinkgoWriter, string(out))
	}

	// Events
	fmt.Fprintln(GinkgoWriter, "\n--- Events ---")
	cmd = exec.Command("kubectl", "get", "events", "-n", ns, "--sort-by=.lastTimestamp")
	out, err = utils.Run(cmd)
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "failed to get events: %v\n", err)
	} else {
		fmt.Fprintln(GinkgoWriter, string(out))
	}

	fmt.Fprintln(GinkgoWriter, "=== END DIAGNOSTIC DUMP ===")
}

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

			By("waiting for CiliumNetworkPolicy CRD to be fully established")
			cmd = exec.Command("kubectl", "wait", "--for=condition=Established",
				"crd/ciliumnetworkpolicies.cilium.io", "--timeout=60s")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("deploying the controller-manager")
			cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectimage))
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			// In DinD (Docker-in-Docker) CI environments, pods cannot reach the
			// Kubernetes API server via its ClusterIP (10.96.0.1) because kube-proxy
			// iptables rules don't function correctly. We work around this by:
			// 1. Disabling leader election (single replica, avoids 5s ClusterIP timeout)
			// 2. Overriding KUBERNETES_SERVICE_HOST/PORT to point directly at the API
			//    server's pod IP, bypassing ClusterIP entirely.
			By("getting API server endpoint for direct connectivity (bypasses broken ClusterIP in DinD)")
			cmd = exec.Command("kubectl", "get", "endpoints", "kubernetes", "-n", "default",
				"-o", `jsonpath={.subsets[0].addresses[0].ip}`)
			apiHost, err := utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			ExpectWithOffset(1, string(apiHost)).NotTo(BeEmpty(), "could not determine API server IP")

			cmd = exec.Command("kubectl", "get", "endpoints", "kubernetes", "-n", "default",
				"-o", `jsonpath={.subsets[0].ports[0].port}`)
			apiPort, err := utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			ExpectWithOffset(1, string(apiPort)).NotTo(BeEmpty(), "could not determine API server port")

			By("patching controller: disable leader election + direct API server endpoint (DinD workaround)")
			patch := fmt.Sprintf(
				`[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--leader-elect=false"},`+
					`{"op":"add","path":"/spec/template/spec/containers/0/env","value":[`+
					`{"name":"KUBERNETES_SERVICE_HOST","value":"%s"},`+
					`{"name":"KUBERNETES_SERVICE_PORT","value":"%s"}]}]`,
				string(apiHost), string(apiPort))
			cmd = exec.Command("kubectl", "patch", "deployment",
				"workbench-operator-controller-manager", "-n", namespace,
				"--type=json", "-p", patch)
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("waiting for the controller-manager deployment to be fully ready")
			cmd = exec.Command("kubectl", "rollout", "status",
				"deployment/workbench-operator-controller-manager",
				"-n", namespace, "--timeout=120s")
			_, err = utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())

			By("verifying no container crash loops")
			Eventually(func() error {
				cmd = exec.Command("kubectl", "get", "pods",
					"-l", "control-plane=controller-manager",
					"-n", namespace,
					"-o", "jsonpath={.items[0].status.containerStatuses[0].restartCount}")
				out, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				restarts, err := strconv.Atoi(string(out))
				if err != nil {
					return fmt.Errorf("failed to parse restart count %q: %w", string(out), err)
				}
				if restarts > 0 {
					return fmt.Errorf("controller container has restarted %d times", restarts)
				}
				return nil
			}, 30*time.Second, 2*time.Second).Should(Succeed())
		})
	})

	Context("Network policies", Ordered, func() {
		const testNS = "netpol-test"

		BeforeAll(func() {
			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNS)
			_, _ = utils.Run(cmd)

			By("verifying the controller is actively reconciling with a probe workspace")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "probe-ws", true, nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "workspace", "probe-ws",
					"-n", testNS, "-o",
					`jsonpath={.status.conditions[?(@.type=="NetworkPolicyReady")].status}`)
				out, _ := utils.Run(cmd)
				return string(out)
			}, 120*time.Second, 2*time.Second).ShouldNot(BeEmpty(),
				"controller never reconciled the probe workspace — check controller logs")

			By("cleaning up probe workspace")
			cmd = exec.Command("kubectl", "delete", "workspace", "probe-ws", "-n", testNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
			// Wait for probe CNP to be garbage collected before running tests
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicies", "-n", testNS, "-o", "jsonpath={.items}")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return string(out) == "[]" || string(out) == ""
			}, 60*time.Second, time.Second).Should(BeTrue())
		})

		AfterAll(func() {
			By("removing test namespace")
			cmd := exec.Command("kubectl", "delete", "ns", testNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		AfterEach(func() {
			if CurrentSpecReport().Failed() {
				dumpDiagnostics(testNS)
			}

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
			}, 60*time.Second, time.Second).Should(BeTrue())
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
			}, 60*time.Second, time.Second).Should(Succeed())

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
			}, 60*time.Second, time.Second).Should(Equal("True"))
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
			}, 60*time.Second, time.Second).Should(Equal(3))

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
			}, 60*time.Second, time.Second).Should(BeTrue())
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
			}, 60*time.Second, time.Second).Should(Succeed())

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
			}, 60*time.Second, time.Second).Should(BeTrue())
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
			}, 60*time.Second, time.Second).Should(Equal(2))

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
			}, 60*time.Second, time.Second).Should(Equal(3))
		})
	})
})
