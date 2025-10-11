# [PUBLIC] CPU DRA Driver with Consumable Capacity

**Author:** Praveen Krishna (@pravk03)
**Date:** Aug 21, 2025

## Objective

This document details an architectural update to the CPU DRA Driver, extending it to support **Consumable Capacity** (as described in KEP-5075) for CPU resource modeling and allocation.

______________________________________________________________________

## The Original Design: Individual CPU Devices

The initial version of the driver allocates CPUs as individual, non-consumable devices.

- **Resource Model:** The driver creates one `Device` per logical CPU in a `ResourceSlice`. Each device has attributes detailing its topology (e.g., `numaNode`, `socketID`, `coreType`).
- **Pod Request:** A `ResourceClaim` requests a specific *count* of these devices.
- **Allocation Logic:** The Kubernetes scheduler selects *N* specific devices that satisfy the claim's constraints. The DRA driver on the node simply receives this list of devices and enforces the allocation.

### Key Limitations

- **Poor Scalability:** Fine-grained CPU allocation decisions are made by the central scheduler, which could increase scheduling latency. More importantly, representing every CPU as a device can easily exceed the `ResourceSlice` limit of 128 devices on large nodes, creating a significant scalability bottleneck for the control plane.
- **Scheduling Rigidity:** This model is inflexible. The scheduler must select specific devices, even when a workload only needs a certain *quantity* of CPUs from a topological domain (like a socket). This can lead to unnecessary resource fragmentation.

______________________________________________________________________

## The New Design: CPU Modelling with Consumable Capacity

The new design leverages consumable capacity to model CPUs as a fungible, countable resource within a larger group.

- **Resource Model:** Instead of a device per CPU, the driver publishes one `Device` per CPU socket (or other logical group). This single device advertises a *consumable* quantity of CPUs (e.g., `"dra.cpu/cpu": "32"`).
- **Pod Request:** A `ResourceClaim` requests a *quantity* of the `dra.cpu/cpu` resource.
- **Allocation Logic:** The allocation intelligence is shifted to the node.
  1. The Kubernetes scheduler's role is simplified: it only performs coarse-grained matching (i.e., finding a socket with enough available capacity).
  1. The DRA driver on the node receives the quantity request and uses its topology-aware placement algorithm to decide which *specific* CPUs to allocate.

### Advantages

- **Decoupled Scheduling and Placement:** This separation of concerns improves performance and efficiency. The central scheduler handles high-level availability checks, while the node-level driver performs the detailed, state-aware placement.
- **Production-Grade Placement Logic:** The driver ensures production-grade CPU placement by using the same battle-tested assignment algorithm as the Kubelet's static CPU Manager, sourced directly from the [Kubernetes repository](https://github.com/kubernetes/kubernetes/blob/091f87c10bc3532041b77a783a5f832de5506dc8/pkg/kubelet/cm/cpumanager/cpu_assignment.go#L4). This guarantees that allocations are topology-aware and optimized to minimize resource fragmentation.
- **Improved Scalability:** Publishing one device per logical block (like a socket) drastically reduces the number of API objects the control plane needs to manage, solving the scalability problem of the previous model.

### Limitations of the Consumable Capacity Model

A key limitation is that a single resource request within a claim cannot exceed the capacity of an individual device pool. For example, on a machine with 32 CPUs per socket, a request for 50 CPUs must be manually split by the user into multiple smaller requests within the `ResourceClaim`, each fitting within a single socket's capacity. This is inflexible and requires users to be aware of the underlying node topology.

______________________________________________________________________

## Implementation Details

The implementation follows the same high-level design as before, but the responsibility of the DRA hook is significantly expanded to handle fine-grained CPU allocation.

#### Driver Configuration

A configuration option (`--cpu-device-mode`) is introduced to select between the `individual` and `grouped` (consumable capacity) models.

#### DRA Hook

- **Resource Publishing:** In consumable mode, the driver creates one `Device` per socket, advertising the total CPU count in the `capacity` field and marking it for multiple allocations.
- **Prepare Resource Claim:**
  1. Identifies the target socket and the number of CPUs requested from the claim's status.
  1. Queries the internal state to find the currently available CPUs on that socket.
  1. Invokes the CPU assignment logic (sourced from the Kubelet) to select the best specific CPUs.
  1. Records the new allocation and generates the necessary CDI file, which makes the `cpuset` available to the NRI hook for enforcement.
- **Unprepare Resource Claim:** When a claim is released, the driver moves the CPUs from the guaranteed pool back to the shared pool, making them available for future claims.

#### NRI Hook

- **Startup (Synchronize):** On startup, the hook inspects all currently running containers to rebuild its internal state. By reading allocation details from each container's environment, it reconstructs its view of which CPUs are allocated and which are available.
- **Container Creation:**
  - The hook determines if a container is "guaranteed" (with a DRA claim) or "shared" by inspecting its environment variables.
  - **For a guaranteed container:** It applies the specific `cpuset` that was pre-allocated during the "prepare" phase. It then triggers an update for all other shared-pool containers, shrinking their CPU sets to exclude the newly allocated guaranteed CPUs.
  - **For a shared container:** It assigns the container the current set of available shared CPUs.
- **Container Removal:**
  - If a guaranteed container is removed, the hook flags that the shared CPU pool needs to be expanded. This expansion is deferred until the next container creation event, as the `RemoveContainer` hook cannot trigger updates on other running containers.

______________________________________________________________________

## Appendix: Example Objects

*The machine has 2 Sockets and 2 NUMA nodes with 32 CPUs on each socket.*

### ResourceSlice

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: kind-worker2-dra.cpu-wg5xw
spec:
  driver: dra.cpu
  nodeName: kind-worker2
  pool:
    generation: 1
    name: kind-worker2
    resourceSliceCount: 1
  devices:
  - name: cpudevsocket0
    allowMultipleAllocations: true
    capacity:
      "dra.cpu/cpu": "32"
    attributes:
      "dra.cpu/socketID": 0
  - name: cpudevsocket1
    allowMultipleAllocations: true
    capacity:
      "dra.cpu/cpu": "32"
    attributes:
      "dra.cpu/socketID": 1
```

### ResourceClaim (Requesting 10 CPUs)

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpu-request-10-cpus
  namespace: default
spec:
  devices:
    requests:
    - name: req-0
      deviceClassName: dra.cpu
      count: 1
      capacity:
        requests:
          "dra.cpu/cpu": "10"
```

### ResourceClaim (Requesting 50 CPUs, split manually)

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpu-request-50-cpus
  namespace: default
spec:
  devices:
    requests:
    - name: req-0
      deviceClassName: dra.cpu
      count: 1
      capacity:
        requests:
          "dra.cpu/cpu": "30"
    - name: req-1
      deviceClassName: dra.cpu
      count: 1
      capacity:
        requests:
          "dra.cpu/cpu": "20"
```
