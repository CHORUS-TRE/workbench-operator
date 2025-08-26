# Technical Context: workbench-operator

## Core Technologies
- **Go**: The operator is written in Go.
- **Kubernetes**: The operator is designed to run on a Kubernetes cluster.
- **Kubebuilder**: The project is scaffolded using the Kubebuilder framework.
- **Docker**: Container images are built using Docker.
- **Xpra**: The core of the workbench is the Xpra HTML5 client, which provides remote desktop capabilities.

## Prerequisites
- Go v1.22.0+
- Docker 17.03+
- kubectl v1.11.3+
- A running Kubernetes v1.29+ cluster

## Development Setup
1.  **Build and Push Image**:
    ```sh
    make docker-build docker-push IMG=<some-registry>/workbench-operator:tag
    ```
2.  **Install CRDs**:
    ```sh
    make install
    ```
3.  **Deploy Manager**:
    ```sh
    make deploy IMG=<some-registry>/workbench-operator:tag
    ```
4.  **Create Instances**:
    ```sh
    kubectl apply -k config/samples/
    ``` 

## Local Development Setup (macOS/colima)

For a complete local development environment on macOS, the following steps are recommended:

1.  **Install Tools**:
    ```sh
    brew install docker colima docker-buildx docker-compose
    ```

2.  **Configure Docker**:
    Ensure your `~/.docker/config.json` is correctly configured to use `colima` and the `docker-buildx` plugin.

3.  **Start Colima**:
    ```sh
    colima start --cpu 8 --memory 12 --disk 300 --kubernetes --kubernetes-version v1.31.5+k3s1
    ```

4.  **Configure Hostfile**:
    Add the colima cluster IP to your `/etc/hosts` file:
    ```
    <colima-ip> workbench.chorus-tre.local
    ```
    You can get the IP with `colima ls`.

5.  **Install Ingress Controller**:
    ```sh
    helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
    helm repo update
    helm install ingress-nginx ingress-nginx/ingress-nginx \
      --namespace ingress-nginx \
      --create-namespace \
      --set controller.service.type=ClusterIP \
      --set controller.hostNetwork=true
    ```

6.  **Deploy Operator**:
    Create a namespace and deploy the operator:
    ```sh
    kubectl create namespace workbench
    make docker-build IMG=harbor.build.chorus-tre.local/chorus/workbench-operator:0.3.8
    make install
    make run
    ``` 