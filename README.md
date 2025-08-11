# dra-driver-cpu

Kubernetes Device Resource Assignment (DRA) driver for CPU resources.

This repository implements a DRA driver that enables Kubernetes clusters to manage and assign CPU resources to workloads using the DRA framework.

## How it Works

The driver is deployed as a DaemonSet which contains two core components:

- **DRA driver**: This component is responsible for discovering the CPU topology
  of the node and reporting the available CPUs as allocatable resources to the
  Kubernetes scheduler by creating `ResourceSlice` objects. When a resource
  claim is allocated, the driver generates a CDI (Container Device Interface)
  specification that tells the container runtime to inject an environment
  variable with the assigned CPU set into the container.

- **NRI Plugin**: This component integrates with the container runtime via the
  Node Resource Interface (NRI).

  - For containers with **guaranteed CPUs**, the plugin reads the environment
    variable injected via CDI and pins the container to its exclusive CPU set.
  - For all other containers, it confines them to a **shared pool** of CPUs that
    are not exclusively allocated.
  - It dynamically updates the shared pool as guaranteed containers are created
    or removed, ensuring efficient use of resources.

## Feature Support

### Currently Supported

- Exclusive CPU Allocation: Guaranteed pods that request CPUs via a
  ResourceClaim are allocated exclusive cores.
- Shared CPU Pool Management: All other containers without a ResourceClaim are
  confined to a shared pool of CPUs that are not reserved for Guaranteed pods.
- Handle daemonset restart. On restart, the driver synchronizes with all
  existing pods on the node to rebuild its state of CPU allocations, ensuring
  accurate CPU allocation for newly scheduled pods.

### Not Supported

- This driver currently only manages CPU resources. Memory allocation and
  management are not supported.

## Getting Started

### Installation

- If needed, create a kind cluster. We have one in the repo, if needed, that
  can be deplayed as follows:
  - `make kind-cluster`
- Deploy the driver and all necessary RBAC configurations using the provided
  manifest
  - `kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/dra-driver-cpu/refs/heads/main/install.yaml`

### Example Usage

- Create a ResourceClaim: This requests a specific number of exclusive CPUs from
  the driver.
  - `kubectl apply -f hack/examples/sample_cpu_resource_claims.yaml`
- Create a Pod: Reference the ResourceClaim in your pod spec to receive the
  allocated CPUs.
  - `kubectl apply -f hack/examples/sample_pod_with_cpu_resource_claim.yaml`

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/)
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

This project is managed by its [OWNERS](https://git.k8s.io/community/contributors/guide/owners.md) and is licensed under [Creative Commons 4.0](https://git.k8s.io/website/LICENSE).
