package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/CHORUS-TRE/workbench-operator/test/utils"
)

const namespace = "workbench-operator-system"

// workspaceManifest returns a reader producing a Workspace YAML manifest for
// use with kubectl apply -f -.
func workspaceManifest(namespace, name, networkPolicy string, allowedFQDNs []string) io.Reader {
	fqdnJSON := "[]"
	if len(allowedFQDNs) > 0 {
		b, err := json.Marshal(allowedFQDNs)
		if err != nil {
			panic(fmt.Sprintf("failed to marshal allowedFQDNs: %v", err))
		}
		fqdnJSON = string(b)
	}

	manifest := fmt.Sprintf(`apiVersion: default.chorus-tre.ch/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  networkPolicy: %s
  allowedFQDNs: %s
`, name, namespace, networkPolicy, fqdnJSON)

	return strings.NewReader(manifest)
}

// workspaceWithServiceManifest returns a Workspace manifest that includes a postgres service entry.
// The chart registry and project are resolved from the operator's --registry and --services-repository flags.
func workspaceWithServiceManifest(namespace, name, networkPolicy, serviceState string) io.Reader {
	manifest := fmt.Sprintf(`apiVersion: default.chorus-tre.ch/v1alpha1
kind: Workspace
metadata:
  name: %s
  namespace: %s
spec:
  networkPolicy: %s
  services:
    postgres:
      state: %s
      chart:
        repository: services/postgres
        tag: "0.0.3"
      credentials:
        secretName: postgres-creds
        paths:
          - settings.superuserPassword
      values:
        settings:
          superuser: postgres
          superuserDatabase: devdb
        storage:
          requestedSize: 1Gi
      connectionInfoTemplate: "postgresql://postgres@{{.ReleaseName}}.{{.Namespace}}:5432/devdb (secret: {{.SecretName}})"
`, name, namespace, networkPolicy, serviceState)

	return strings.NewReader(manifest)
}

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
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	AfterAll(func() {
		By("undeploying the controller-manager")
		cmd := exec.Command("make", "undeploy-e2e")
		if _, err := utils.Run(cmd); err != nil {
			fmt.Fprintf(GinkgoWriter, "warning: undeploy-e2e failed: %v\n", err)
		}

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall", "ignore-not-found=true")
		if _, err := utils.Run(cmd); err != nil {
			fmt.Fprintf(GinkgoWriter, "warning: uninstall CRDs failed: %v\n", err)
		}

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found")
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

			By("deploying the controller-manager (test overlay, no Prometheus dependency)")
			cmd = exec.Command("make", "deploy-e2e", fmt.Sprintf("IMG=%s", projectimage))
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

			By("verifying container[0] is the manager (guard against index-based patch breakage)")
			cmd = exec.Command("kubectl", "get", "deployment",
				"workbench-operator-controller-manager", "-n", namespace,
				"-o", `jsonpath={.spec.template.spec.containers[0].name}`)
			containerName, err := utils.Run(cmd)
			ExpectWithOffset(1, err).NotTo(HaveOccurred())
			ExpectWithOffset(1, string(containerName)).To(Equal("manager"),
				"expected containers[0] to be 'manager', got %q — JSON patch indices need updating", string(containerName))

			// Build the DinD patch: leader election + direct API server endpoint.
			// When E2E_REGISTRY is set, also configure the operator's --registry flag
			// so that service tests can pull Helm charts from an accessible registry.
			patchOps := fmt.Sprintf(
				`[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--leader-elect=false"},`+
					`{"op":"add","path":"/spec/template/spec/containers/0/env","value":[`+
					`{"name":"KUBERNETES_SERVICE_HOST","value":"%s"},`+
					`{"name":"KUBERNETES_SERVICE_PORT","value":"%s"}]}`,
				string(apiHost), string(apiPort))

			if registry := os.Getenv("E2E_REGISTRY"); registry != "" {
				patchOps += fmt.Sprintf(
					`,{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--registry=%s"}`, registry)
			}
			patchOps += "]"

			By("patching controller: disable leader election + direct API server endpoint (DinD workaround)")
			cmd = exec.Command("kubectl", "patch", "deployment",
				"workbench-operator-controller-manager", "-n", namespace,
				"--type=json", "-p", patchOps)
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
			cmd.Stdin = workspaceManifest(testNS, "probe-ws", "Airgapped", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", "probe-ws",
					"-n", testNS, "-o",
					`jsonpath={.status.conditions[?(@.type=="NetworkPolicyReady")].status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
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

			// Clean up workspaces in test namespace and wait for deletion to complete
			cmd := exec.Command("kubectl", "delete", "workspaces", "--all", "-n", testNS,
				"--ignore-not-found", "--wait=true", "--timeout=30s")
			_, _ = utils.Run(cmd)
			// Wait for CNPs to be garbage collected
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicies", "-n", testNS, "-o", "jsonpath={.items}")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				return string(out) == "[]" || string(out) == ""
			}, 15*time.Second, time.Second).Should(BeTrue())
		})

		It("creates a CiliumNetworkPolicy for an airgapped workspace", func() {
			By("creating an airgapped Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "airgapped-ws", "Airgapped", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is created")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "airgapped-ws-netpol", "-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 60*time.Second, time.Second).Should(Succeed())

			By("verifying CNP has 3 egress rules (kube-dns + intra-namespace pod + intra-namespace service)")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "airgapped-ws-netpol",
				"-n", testNS, "-o", "jsonpath={.spec.egress}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			var egress []map[string]any
			Expect(json.Unmarshal(out, &egress)).To(Succeed())
			Expect(egress).To(HaveLen(3))

			By("verifying NetworkPolicyReady condition is True")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", "airgapped-ws",
					"-n", testNS, "-o",
					`jsonpath={.status.conditions[?(@.type=="NetworkPolicyReady")].status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 60*time.Second, time.Second).Should(Equal("True"))
		})

		It("creates a CiliumNetworkPolicy restricting egress to specified FQDNs", func() {
			By("creating a FQDNAllowlist Workspace")
			fqdns := []string{"example.com", "*.corp.internal"}
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "fqdn-ws", "FQDNAllowlist", fqdns)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is created with 4 egress rules")
			Eventually(func() (int, error) {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "fqdn-ws-netpol",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return 0, err
				}
				var egress []map[string]any
				if err := json.Unmarshal(out, &egress); err != nil {
					return 0, err
				}
				return len(egress), nil
			}, 60*time.Second, time.Second).Should(Equal(4))

			By("verifying the FQDN rule contains expected selectors")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "fqdn-ws-netpol",
				"-n", testNS, "-o", "jsonpath={.spec.egress[3].toFQDNs}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring("example.com"))
			Expect(string(out)).To(ContainSubstring("*.corp.internal"))
		})

		It("creates a CiliumNetworkPolicy allowing full internet for Open workspace", func() {
			By("creating an Open Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "open-ws", "Open", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP has toCIDR rule for internet access")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "open-ws-netpol",
					"-n", testNS, "-o", "jsonpath={.spec.egress[3].toCIDR}")
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 60*time.Second, time.Second).ShouldNot(BeEmpty())
		})

		It("sets owner reference so CNP is garbage-collected with workspace", func() {
			By("creating a Workspace")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "owner-ws", "Airgapped", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for CNP creation")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-netpol", "-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 60*time.Second, time.Second).Should(Succeed())

			By("verifying owner reference on CNP")
			cmd = exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-netpol",
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
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "owner-ws-netpol",
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
			cmd.Stdin = workspaceManifest(testNS, "update-ws", "Airgapped", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for airgapped CNP (3 egress rules)")
			Eventually(func() (int, error) {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "update-ws-netpol",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return 0, err
				}
				var egress []map[string]any
				if err := json.Unmarshal(out, &egress); err != nil {
					return 0, err
				}
				return len(egress), nil
			}, 60*time.Second, time.Second).Should(Equal(3))

			By("switching workspace to Open")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "update-ws", "Open", nil)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying CNP is updated to 4 egress rules")
			Eventually(func() (int, error) {
				cmd := exec.Command("kubectl", "get", "ciliumnetworkpolicy", "update-ws-netpol",
					"-n", testNS, "-o", "jsonpath={.spec.egress}")
				out, err := utils.Run(cmd)
				if err != nil {
					return 0, err
				}
				var egress []map[string]any
				if err := json.Unmarshal(out, &egress); err != nil {
					return 0, err
				}
				return len(egress), nil
			}, 60*time.Second, time.Second).Should(Equal(4))
		})
	})

	Context("Workspace services", Ordered, func() {
		const testNS = "services-test"
		const wsName = "svc-test-ws"
		const releaseName = wsName + "-postgres"

		BeforeAll(func() {
			if os.Getenv("E2E_REGISTRY") == "" {
				Skip("E2E_REGISTRY not set — skipping service tests (no accessible Helm registry)")
			}

			By("creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNS)
			_, _ = utils.Run(cmd)

			By("verifying the controller is actively reconciling with a probe workspace")
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceManifest(testNS, "probe-ws", "Airgapped", nil)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", "probe-ws",
					"-n", testNS, "-o",
					`jsonpath={.status.conditions[?(@.type=="NetworkPolicyReady")].status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 120*time.Second, 2*time.Second).ShouldNot(BeEmpty())

			By("cleaning up probe workspace")
			cmd = exec.Command("kubectl", "delete", "workspace", "probe-ws", "-n", testNS, "--ignore-not-found")
			_, _ = utils.Run(cmd)
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
		})

		It("deploys postgres and reports Running status", func() {
			By("creating a workspace with a postgres service in state Running")
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = workspaceWithServiceManifest(testNS, wsName, "Airgapped", "Running")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for status.services.postgres.status == Running")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", wsName,
					"-n", testNS, "-o",
					`jsonpath={.status.services.postgres.status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 120*time.Second, 2*time.Second).Should(Equal("Running"))

			By("verifying credential secret was created")
			cmd = exec.Command("kubectl", "get", "secret", "postgres-creds", "-n", testNS)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("verifying connectionInfo is populated")
			cmd = exec.Command("kubectl", "get", "workspace", wsName,
				"-n", testNS, "-o",
				`jsonpath={.status.services.postgres.connectionInfo}`)
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring(releaseName))
		})

		It("stops the postgres service while retaining the PVC", func() {
			By("patching the service state to Stopped")
			cmd := exec.Command("kubectl", "patch", "workspace", wsName,
				"-n", testNS, "--type=merge",
				`--patch={"spec":{"services":{"postgres":{"state":"Stopped"}}}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for status.services.postgres.status == Stopped")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", wsName,
					"-n", testNS, "-o",
					`jsonpath={.status.services.postgres.status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 60*time.Second, 2*time.Second).Should(Equal("Stopped"))

			By("verifying no pods remain for the release")
			cmd = exec.Command("kubectl", "get", "pods",
				"-l", "app.kubernetes.io/instance="+releaseName,
				"-n", testNS, "-o", "jsonpath={.items}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			var items []any
			_ = json.Unmarshal(out, &items)
			Expect(items).To(BeEmpty())

			By("verifying PVC is still present")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "pvc",
					"-l", "app.kubernetes.io/instance="+releaseName,
					"-n", testNS)
				_, err := utils.Run(cmd)
				return err
			}, 10*time.Second, time.Second).Should(Succeed())
		})

		It("restarts the postgres service reusing the existing PVC", func() {
			By("patching the service state back to Running")
			cmd := exec.Command("kubectl", "patch", "workspace", wsName,
				"-n", testNS, "--type=merge",
				`--patch={"spec":{"services":{"postgres":{"state":"Running"}}}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for status.services.postgres.status == Running")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", wsName,
					"-n", testNS, "-o",
					`jsonpath={.status.services.postgres.status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 120*time.Second, 2*time.Second).Should(Equal("Running"))
		})

		It("deletes the postgres service and its PVC", func() {
			By("patching the service state to Deleted")
			cmd := exec.Command("kubectl", "patch", "workspace", wsName,
				"-n", testNS, "--type=merge",
				`--patch={"spec":{"services":{"postgres":{"state":"Deleted"}}}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for status.services.postgres.status == Deleted")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", wsName,
					"-n", testNS, "-o",
					`jsonpath={.status.services.postgres.status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 60*time.Second, 2*time.Second).Should(Equal("Deleted"))

			By("verifying PVC is deleted")
			Eventually(func() bool {
				cmd := exec.Command("kubectl", "get", "pvc",
					"-l", "app.kubernetes.io/instance="+releaseName,
					"-n", testNS, "-o", "jsonpath={.items}")
				out, err := utils.Run(cmd)
				if err != nil {
					return false
				}
				var items []any
				_ = json.Unmarshal(out, &items)
				return len(items) == 0
			}, 60*time.Second, 2*time.Second).Should(BeTrue())
		})

		It("re-deploys postgres from Deleted state (fresh install)", func() {
			By("patching the service state back to Running")
			cmd := exec.Command("kubectl", "patch", "workspace", wsName,
				"-n", testNS, "--type=merge",
				`--patch={"spec":{"services":{"postgres":{"state":"Running"}}}}`)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())

			By("waiting for status.services.postgres.status == Running")
			Eventually(func() (string, error) {
				cmd := exec.Command("kubectl", "get", "workspace", wsName,
					"-n", testNS, "-o",
					`jsonpath={.status.services.postgres.status}`)
				out, err := utils.Run(cmd)
				if err != nil {
					return "", err
				}
				return string(out), nil
			}, 120*time.Second, 2*time.Second).Should(Equal("Running"))

			By("verifying connectionInfo is populated for the fresh install")
			cmd = exec.Command("kubectl", "get", "workspace", wsName,
				"-n", testNS, "-o",
				`jsonpath={.status.services.postgres.connectionInfo}`)
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(ContainSubstring(releaseName))
		})
	})
})
