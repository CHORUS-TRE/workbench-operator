# Project Brief: workbench-operator

## Core Mission
The `workbench-operator` is a Kubernetes operator designed to simplify the management and deployment of user-specific workbenches. It automates the setup of Xpra servers, graphical applications, and associated Kubernetes resources, allowing users to focus on their work rather than on infrastructure configuration.

## Key Components
A `Workbench` custom resource (CR) is the central element managed by the operator. Each `Workbench` instance consists of:
- **Deployment**: Manages the Xpra server pod.
- **Service**: Exposes the Xpra server via HTTP and an X11 socket.
- **Jobs**: Run individual graphical applications within the workbench environment. 