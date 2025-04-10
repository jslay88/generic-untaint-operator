# generic-untaint-operator

A Kubernetes operator that automatically removes taints from nodes when specified workloads (primarily DaemonSets) become ready on the node. This is particularly useful when nodes are created with taints (e.g., via Karpenter's NodePool startupTaint option) and you need those taints to be removed once critical workloads are running.

## Description

The generic-untaint-operator watches nodes with a specific taint and removes that taint when all specified workloads (identified by their names) have ready pods running on the node. This operator is designed to work with any workload type, but is primarily intended for use with DaemonSets that don't provide their own untaint capabilities.

Common use cases include:
- Working with Karpenter-provisioned nodes that use startupTaint
- Managing nodes that need to be tainted until critical system workloads are ready
- Automating the removal of taints that were added during node provisioning

## Getting Started

### Prerequisites
- go version v1.22.0+
- docker version 17.03+
- kubectl version v1.11.3+
- Access to a Kubernetes v1.11.3+ cluster

### Configuration

The operator is configured through command-line flags:

- `--target-taint`: The key of the taint to watch for and remove (required)
- `--owned-by-names`: Comma-separated list of workload names to check for readiness (required)

Example configuration:
```yaml
args:
  - --target-taint=jslay88.github.io/not-ready
  - --owned-by-names=some-daemonset,another-daemonset
```

#### Finding the Correct Owned-by-names Value

To determine the correct value for `--owned-by-names`, you need to inspect the pods that should trigger the taint removal. The value should match the name of the workload (e.g., DaemonSet) that owns the pods.

1. List the pods running on a node with the taint:
```sh
kubectl get pods -o wide | grep <node-name>
```

2. For each pod you want to monitor, inspect its owner references:
```sh
kubectl get pod <pod-name> -o jsonpath='{.metadata.ownerReferences[*].name}'
```

For example, if you have a DaemonSet named `my-daemonset` that you want to monitor:
```sh
# First, find a pod owned by the DaemonSet
kubectl get pods -o wide | grep my-daemonset

# Then inspect its owner reference
kubectl get pod my-daemonset-abc123 -o jsonpath='{.metadata.ownerReferences[*].name}'
# Output: my-daemonset
```

In this case, you would set `--owned-by-names=my-daemonset` in the operator configuration.

You can also use `kubectl describe pod` to see the owner references in a more readable format:
```sh
kubectl describe pod <pod-name>
# Look for the "Owner References" section in the output
```

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/generic-untaint-operator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don't work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/generic-untaint-operator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

### Example Usage with Karpenter

1. Create a Karpenter NodePool with a startupTaint:
```yaml
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      startupTaints:
      - key: jslay88.github.io/not-ready
        value: "true"
        effect: NoSchedule
```

2. Deploy the generic-untaint-operator configured to watch for this taint:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: generic-untaint-operator
spec:
  template:
    spec:
      containers:
      - name: manager
        args:
        - --target-taint=jslay88.github.io/not-ready
        - --owned-by-names=some-daemonset,another-daemonset
```

3. When Karpenter provisions a new node:
   - The node will be created with the specified taint
   - The generic-untaint-operator will watch for the specified workloads
   - Once all specified workloads have ready pods on the node, the taint will be automatically removed

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
make build-installer IMG=<some-registry>/generic-untaint-operator:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/generic-untaint-operator/<tag or branch>/dist/install.yaml
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

