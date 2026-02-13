# Contributing to workbench-operator

This guide walks you through **local testing** of the operator on macOS using [Colima](https://github.com/abiosoft/colima) + k3s. It covers tool installation, cluster setup, building and deploying the operator, and verifying it works end-to-end.

> **Intel and Apple Silicon** — every step is the same on both architectures. Docker will build native images automatically.

---

## Prerequisites

- macOS with [Homebrew](https://brew.sh).
- `brew` in your shell PATH.
- Go 1.25+ (for `make test` / `make docker-build`).

---

## 1) Install tools

```bash
brew update
brew install docker colima docker-buildx docker-compose docker-credential-helper kubectl helm
```

Configure Docker to use the macOS keychain (avoids credential prompts and Docker Desktop leftovers):

```bash
# Merge into existing config — safe if ~/.docker/config.json already exists
mkdir -p ~/.docker
touch ~/.docker/config.json
jq -s '.[0] * {credsStore: "osxkeychain"}' ~/.docker/config.json > /tmp/docker-cfg.json \
  && mv /tmp/docker-cfg.json ~/.docker/config.json
```

> If `~/.docker/config.json` does not exist yet you can simply run:
> ```bash
> echo '{"credsStore":"osxkeychain"}' > ~/.docker/config.json
> ```

---

## 2) Wire Docker CLI plugins

Homebrew does not symlink `docker-buildx` and `docker-compose` into the CLI plugin directory automatically:

```bash
mkdir -p ~/.docker/cli-plugins

ln -sfn "$(brew --prefix docker-buildx)/bin/docker-buildx" \
  ~/.docker/cli-plugins/docker-buildx

ln -sfn "$(brew --prefix docker-compose)/bin/docker-compose" \
  ~/.docker/cli-plugins/docker-compose
```

Verify:

```bash
docker buildx version   # github.com/docker/buildx …
docker compose version  # Docker Compose version …
```

---

## 3) Start Colima with k3s

```bash
colima start \
  --cpu 4 \
  --memory 12 \
  --disk 50 \
  --kubernetes \
  --kubernetes-version v1.33.7+k3s3 \
  --network-address
```

Switch context and verify:

```bash
docker context use colima
kubectl config use-context colima
```

**Known issue** — with `--network-address`, Colima writes the VM bridge IP into the kubeconfig. k3s port-forwards through `localhost`, so you may need to patch the server URL:

```bash
# If `kubectl get nodes` fails with "no route to host":
PORT=$(kubectl config view -o jsonpath='{.clusters[?(@.name=="colima")].cluster.server}' | grep -oE '[0-9]+$')
kubectl config set-cluster colima --server="https://127.0.0.1:${PORT}"
```

You should now see:

```
$ kubectl get nodes
NAME     STATUS   ROLES                  AGE   VERSION
colima   Ready    control-plane,master   …     v1.33.7+k3s3
```

---

## 4) Install ingress-nginx (optional)

Only needed if you want to expose a Workbench via HTTP from the host:

```bash
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo update

helm install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress-nginx \
  --create-namespace \
  --set controller.service.type=ClusterIP \
  --set controller.hostNetwork=true

kubectl wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=120s
```

Traffic reaches the cluster via `http://127.0.0.1` (port 80/443 on the Colima VM, forwarded to the host).

---

## 5) Get the source

```bash
git clone https://github.com/CHORUS-TRE/workbench-operator.git
cd workbench-operator
```

---

## 6) Build the operator image and load it into k3s

Colima runs Docker and k3s in the same VM, but they use **separate containerd namespaces** (`moby` vs `k8s.io`). A `docker build` produces an image Docker can see, but k3s cannot — you need to import it explicitly.

```bash
export IMG=localhost/workbench-operator:dev

# Build
make docker-build IMG="$IMG"

# Import into k3s's containerd (note: k3s has its own socket, separate from Docker's)
docker save "$IMG" \
  | colima ssh -- sudo k3s ctr \
      --address /run/k3s/containerd/containerd.sock \
      -n k8s.io images import -
```

Verify the image is visible to k3s:

```bash
colima ssh -- sudo k3s crictl images | grep workbench
```

---

## 7) Install CRDs and deploy

```bash
make install
make deploy IMG="$IMG"
```

> **Note:** `make deploy` may print a `ServiceMonitor` error if the Prometheus CRD is not installed. This is harmless — the operator itself runs fine without it.

Verify:

```bash
kubectl get pods -n workbench-operator-system
# NAME                                                    READY   STATUS    …
# workbench-operator-controller-manager-…                 1/1     Running   …
```

### Install the Cilium CRD (for workspace network policies)

The workspace controller watches `CiliumNetworkPolicy` resources. In production, Cilium provides this CRD. On a local cluster without Cilium, apply the minimal stub CRD:

```bash
kubectl apply -f config/crd/thirdparty/cilium.io_ciliumnetworkpolicies.yaml
```

Confirm the workspace controller starts (check logs):

```bash
kubectl logs -n workbench-operator-system \
  deploy/workbench-operator-controller-manager -c manager --tail=5
# … Starting Controller  {"controller": "workspace", …}
# … Starting workers     {"controller": "workspace", …}
```

---

## 8) Test with sample resources

### Workspace (network policy)

```bash
kubectl create ns workspace
kubectl apply -f config/samples/default_v1alpha1_workspace.yaml
```

Verify:

```bash
kubectl get workspace -n workspace
# NAME        AIRGAPPED   NETWORK-POLICY   AGE
# workspace   false       True             …

kubectl get ciliumnetworkpolicies -n workspace
# NAME               AGE
# workspace-egress   …
```

#### Validate network policy content

Inspect the generated `CiliumNetworkPolicy` to confirm the egress rules match the workspace spec:

```bash
kubectl get ciliumnetworkpolicy workspace-egress -n workspace -o jsonpath='{.spec}' | jq .
```

The sample workspace has `airgapped: false` with `allowedFQDNs: ["chorus-tre.ch"]`, so you should see **three egress rules**:

1. **DNS** — allows UDP/TCP 53 to `kube-system` (kube-dns)
2. **Intra-namespace** — allows all pod-to-pod traffic within the workspace namespace
3. **FQDN allowlist** — allows HTTP/HTTPS to `chorus-tre.ch` and `*.chorus-tre.ch`

```json
{
  "egress": [
    { "toEndpoints": [{"matchLabels": {"k8s:io.kubernetes.pod.namespace": "kube-system"}}],
      "toPorts": [{"ports": [{"port": "53", "protocol": "UDP"}, {"port": "53", "protocol": "TCP"}],
                   "rules": {"dns": [{"matchPattern": "*"}]}}] },
    { "toEndpoints": [{"matchLabels": {}}] },
    { "toFQDNs": [{"matchName": "chorus-tre.ch"}, {"matchPattern": "*.chorus-tre.ch"}],
      "toPorts": [{"ports": [{"port": "80", "protocol": "TCP"}, {"port": "443", "protocol": "TCP"}],
                   "rules": {"http": [{}]}}] }
  ],
  "endpointSelector": {"matchLabels": {}}
}
```

You can also verify the owner reference (ensures the CNP is garbage-collected when the workspace is deleted):

```bash
kubectl get ciliumnetworkpolicy workspace-egress -n workspace \
  -o jsonpath='{.metadata.ownerReferences[0].kind}/{.metadata.ownerReferences[0].name}'
# Workspace/workspace
```

> **Note:** Without Cilium installed in the cluster, the CNP is just a stored resource — network isolation is **not enforced**. See [Appendix A](#appendix-a-cilium-enforcement-testing) if you want to test actual traffic blocking locally.

### Workbench (full stack)

The Workbench sample references container images from the internal harbor registry. To test locally, edit `config/samples/default_v1alpha1_workbench.yaml` and replace the image references with images you have access to, then:

```bash
kubectl apply -f config/samples/default_v1alpha1_workbench.yaml
```

---

## 9) Expose a Workbench via Ingress (optional)

If you deployed ingress-nginx (§4) and have a running Workbench, apply the sample Ingress:

```bash
kubectl apply -f config/samples/ingress.yaml
```

Then open `http://127.0.0.1/` or map the hostname in `/etc/hosts`:

```bash
# /etc/hosts
127.0.0.1  workbench-sample.chorus-tre.local
```

---

## 10) Day-to-day commands

```bash
# Start / stop the VM
colima start --kubernetes --network-address
colima stop

# Rebuild and reload after code changes
make docker-build IMG="$IMG"
docker save "$IMG" | colima ssh -- sudo k3s ctr --address /run/k3s/containerd/containerd.sock -n k8s.io images import -
kubectl rollout restart deploy/workbench-operator-controller-manager -n workbench-operator-system

# Run unit tests (no cluster needed)
make test

# Operator logs
kubectl logs -n workbench-operator-system \
  deploy/workbench-operator-controller-manager -c manager -f
```

---

## 11) Clean up

```bash
kubectl delete -f config/samples/default_v1alpha1_workspace.yaml --ignore-not-found
kubectl delete -k config/samples/ --ignore-not-found
make undeploy
make uninstall
```

Optionally destroy the entire VM:

```bash
colima delete -f
```

---

## Running tests

| Target | What it runs | Cluster needed? |
|---|---|---|
| `make test` | Controller tests via [envtest](https://book.kubebuilder.io/reference/envtest) (in-memory API server) | No |
| `make test-e2e` | End-to-end tests against a live cluster (Colima, Kind, …) | Yes |
| `make lint` | `golangci-lint` static analysis | No |

```bash
# Unit / integration tests (no cluster)
make test

# End-to-end (requires a running cluster in kubeconfig)
make test-e2e

# Lint
make lint
```

---

## Appendix A: Cilium enforcement testing

The main guide (§7–§8) uses a **stub CRD** so the workspace controller can create `CiliumNetworkPolicy` objects without installing Cilium. The policies are stored but not enforced. This appendix shows how to set up a cluster with real Cilium so you can verify that traffic is actually blocked.

### A.1) Create a Cilium-ready cluster

Cilium replaces the default k3s CNI (Flannel), so you must disable it at cluster creation time. If you already have a running Colima cluster, tear it down first:

```bash
# Clean up any existing deployment
kubectl delete -f config/samples/default_v1alpha1_workspace.yaml --ignore-not-found
make undeploy || true
make uninstall || true
colima delete -f
```

Start a new cluster with Flannel, the built-in network policy controller, Traefik, and ServiceLB disabled:

```bash
colima start \
  --cpu 4 \
  --memory 12 \
  --disk 50 \
  --kubernetes \
  --kubernetes-version v1.33.7+k3s3 \
  --network-address \
  --k3s-arg='--flannel-backend=none' \
  --k3s-arg='--disable-network-policy' \
  --k3s-arg='--disable=traefik,servicelb'
```

Fix the kubeconfig (same `--network-address` issue as §3):

```bash
docker context use colima
kubectl config use-context colima
PORT=$(kubectl config view -o jsonpath='{.clusters[?(@.name=="colima")].cluster.server}' | grep -oE '[0-9]+$')
kubectl config set-cluster colima --server="https://127.0.0.1:${PORT}"
```

The node will show `Ready` even without a CNI, but pods won't get networking until Cilium is installed.

### A.2) Install Cilium

```bash
brew install cilium-cli   # if not already installed

cilium install --wait
cilium status --wait
```

You should see:

```
    /¯¯\
 /¯¯\__/¯¯\    Cilium:             OK
 \__/¯¯\__/    Operator:           OK
 /¯¯\__/¯¯\    Envoy DaemonSet:    OK
 \__/¯¯\__/    Hubble Relay:       disabled
    \__/       ClusterMesh:        disabled
```

Cilium installs the real `CiliumNetworkPolicy` CRD — no stub needed.

### A.3) Build, load, and deploy the operator

Same as §6–§7, but note the image needs `--provenance=false` to produce a plain Docker manifest (BuildKit attestation creates an OCI index that `crictl` can't resolve):

```bash
export IMG=localhost/workbench-operator:dev

docker build --provenance=false -t "$IMG" .

docker save "$IMG" \
  | colima ssh -- sudo k3s ctr \
      --address /run/k3s/containerd/containerd.sock \
      -n k8s.io images import -

# Verify
colima ssh -- sudo k3s crictl images | grep workbench

make install
make deploy IMG="$IMG"
```

> `make deploy` will print a `ServiceMonitor` error — harmless.

### A.4) Create a workspace and verify enforcement

```bash
kubectl create ns workspace
kubectl apply -f config/samples/default_v1alpha1_workspace.yaml
```

Confirm the CNP is `VALID` (Cilium validated the policy):

```bash
kubectl get ciliumnetworkpolicies -n workspace
# NAME               AGE   VALID
# workspace-egress   …     True
```

### A.5) Test actual traffic blocking

Spin up a test pod in the workspace namespace:

```bash
kubectl run test-curl -n workspace \
  --image=curlimages/curl --restart=Never -- sleep 3600
kubectl wait -n workspace --for=condition=ready pod/test-curl --timeout=60s
```

**Allowed** — `chorus-tre.ch` (in the workspace's `allowedFQDNs`):

```bash
kubectl exec -n workspace test-curl -- \
  curl -s --connect-timeout 5 -o /dev/null -w "%{http_code}\n" http://chorus-tre.ch
# 301  ← connection succeeded (HTTP redirect to HTTPS)
```

**Allowed** — wildcard subdomain `*.chorus-tre.ch`:

```bash
kubectl exec -n workspace test-curl -- \
  curl -s --connect-timeout 5 -o /dev/null -w "%{http_code}\n" http://docs.chorus-tre.ch
# 301
```

**Blocked** — `google.com` (not in `allowedFQDNs`):

```bash
kubectl exec -n workspace test-curl -- \
  curl -s --connect-timeout 5 -o /dev/null -w "%{http_code}\n" http://google.com
# 000  ← connection timed out (exit code 28)
```

**DNS** works (allowed by the CNP DNS rule):

```bash
kubectl exec -n workspace test-curl -- nslookup chorus-tre.ch
# Server:    10.43.0.10
# Address:   10.43.0.10:53
# …
```

Clean up:

```bash
kubectl delete pod test-curl -n workspace
```

### A.6) Tear down

```bash
kubectl delete -f config/samples/default_v1alpha1_workspace.yaml --ignore-not-found
make undeploy
make uninstall
colima delete -f
```
