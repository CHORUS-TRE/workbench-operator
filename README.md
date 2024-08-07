# workbench-operator

Operator for workbenches.

## Description

The goal is to delegate the hard work of wiring the Xpra server and their applications, as well as the volumes to a controller.

A Workbench is composed of:

- A [Deployment](./internal/controller/deployment.go) + ReplicaSet + Pod for the Xpra server
- A [Service](./internal/controller/service.go) handling the HTTP endpoint, and X11 Socket, of Xpra.
- Many [Jobs](./internal/controller/job.go), one for each app. An App being a graphical application, it will come and go.

```text
                  CRD
                   |
                   v
                operator
                   |
      .------------+-----------+------+- etc.
      |            |           |      |
      v            v           v      v
  Deployment    Service       Job    Job
      |                        |      |
      v                        v      v
  ReplicaSet                  Pod    Pod
      |
      v
     Pod
```

To expose a Workbench on the Internet, an Ingress will be needed. It should point to the service on port 8080.

### Caveats

As a server is a `Deployment`, stopping it from the inside will _restart_ it.

### TODO

- Allow to specify xpra-server and socat images.
- Handling the whole life cycle when the user stops the Server from within;
- Report various information in the /status for the applications;
- TLS between the Xpra server and the applications;
- etc.

## Getting Started

### Prerequisites

- go version v1.22.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster

**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/workbench-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/workbench-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
> privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

> **NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall

**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/workbench-operator:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/workbench-operator/<tag or branch>/dist/install.yaml
```

## Contributing

// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License and Usage Restrictions

Any use of the software for purposes other than academic research, including for commercial purposes, shall be requested in advance from CHUV.
