# dra-driver-cpu

Kubernetes Device Resource Assignment (DRA) driver for CPU resources.

This repository implements a DRA driver that enables Kubernetes clusters to manage and assign CPU resources to workloads using the DRA framework.

## Configuration

The driver can be configured with the following command-line flags:

- `--cpu-device-mode`: Sets the mode for exposing CPU devices.
  - `"individual"`: Exposes each allocatable CPU as a separate device in the `ResourceSlice`. This mode provides fine-grained control as it exposes granular information specific to each CPU as device attributes in the `ResourceSlice`.
  - `"grouped" (default)`: Exposes a single device representing a group of CPUs. This mode treats CPUs as a [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) within the group, improving scalability by reducing the number of API objects.
- `--cpu-device-group-by`: When `--cpu-device-mode` is set to `"grouped"`, this flag determines the grouping strategy.
  - `"numanode"` (default): Groups CPUs by NUMA node.
  - `"socket"`: Groups CPUs by socket.
- `--reserved-cpus`: Specifies a set of CPUs to be reserved for system and kubelet processes. These CPUs will not be allocatable by the DRA driver and would be excluded from the `ResourceSlice`. The value is a cpuset, e.g., `0-1`. This semantic is the same as the one the kubelet applies with its `static` CPU Manager policy and enabling [`strict-cpu-reservation`](https://kubernetes.io/blog/2024/12/16/cpumanager-strict-cpu-reservation/) flag and specifying the CPUs with the [`reservedSystemCPUs`](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#explicitly-reserved-cpu-list) to be reserved for system daemons. For correct CPU accounting, the number of CPUs reserved with this flag should match the sum of the kubelet's `kubeReserved` and `systemReserved` settings. This ensures the kubelet subtracts the correct number of CPUs from `Node.Status.Allocatable`.

## How it Works

The driver is deployed as a DaemonSet which contains two core components:

- **DRA driver**: This component is the main control loop and handles the interaction with the Kubernetes API server for Dynamic Resource Allocation.

  - **Topology Discovery**: It discovers the node's CPU topology, including details like sockets, NUMA nodes, cores, SMT siblings, Last-Level Cache (LLC), and core types (e.g., Performance-cores, Efficiency-cores). This is done by parsing `/proc/cpuinfo` and reading sysfs files.
  - **ResourceSlice Publication**: Based on the `--cpu-device-mode` flag, it publishes `ResourceSlice` objects to the API server:
    - In `individual` mode, each allocatable CPU becomes a device in the `ResourceSlice`, with attributes detailing its topology.
    - In `grouped` mode, devices represent larger CPU aggregates (like NUMA nodes or sockets). These devices support consumable capacity, indicating the number of available CPUs within that group.
  - **Claim Allocation**: When a `ResourceClaim` is assigned to the node, the DRA driver handles the allocation:
    - In `individual` mode, the scheduler has already selected specific CPU devices. The driver enforces this selection through CDI and NRI.
    - In `grouped` mode, the claim requests a *quantity* of CPUs from the group device. The driver then uses topology-aware allocation logic (imported from [Kubelet's CPU Manager](https://github.com/kubernetes/kubernetes/blob/fd5b2efa76e44c5ef523cd0711f5ed23eb7e6b1a/pkg/kubelet/cm/cpumanager/cpu_assignment.go)) to select the physical CPUs within the group. Strict compatibility with kubelet's cpumanager or CPU allocation is not a goal of this driver. This decision will be reviewed in the future releases.
  - **CDI Spec Generation**: Upon successful allocation, the driver generates a CDI (Container Device Interface) specification.

- **CDI (Container Device Interface)**: The driver uses CDI to communicate the allocated CPU set to the container runtime.

  - A CDI JSON spec file is created or updated for the allocated claim.
  - This spec instructs the runtime to inject an environment variable (e.g., `DRA_CPUSET_<claimUID>=<cpuset>`) into the container.
  - The driver includes mechanisms for thread-safe and atomic updates to the CDI spec files.

- **NRI Plugin**: This component integrates with the container runtime via the Node Resource Interface (NRI).

  - For containers with **guaranteed CPUs** (those with a DRA ResourceClaim), the plugin reads the environment variable injected via CDI and pins the container to its exclusive CPU set using the cgroup cpuset controller.
  - For all other containers, it confines them to a **shared pool** of CPUs, which consists of all allocatable CPUs not exclusively assigned to any guaranteed container.
  - It dynamically updates the shared pool cpuset for all shared containers whenever guaranteed allocations change (containers are created or removed).
  - On restart, the NRI plugin can synchronize its state by inspecting existing containers and their environment variables to rebuild the current CPU allocations.

## Feature Support

### Currently Supported

- **Exclusive CPU Allocation**: Pods that request CPUs via a ResourceClaim are allocated exclusive CPUs based on the chosen mode and topology.
- **Shared CPU Pool Management**: All other containers without a ResourceClaim are confined to a shared pool of CPUs that are not reserved.
- **Topology Awareness**: The driver discovers detailed CPU topology including sockets, NUMA nodes, cores, SMT siblings, L3 cache (UncoreCache), and core types (Performance/Efficiency).
- **Advanced CPU Allocation Strategies**: When in `"grouped"` mode, the driver utilizes allocation logic adapted from the Kubelet's CPU Manager, including:
  - NUMA aware best-fit allocation.
  - Packing or spreading CPUs across cores.
  - Preference for aligning allocations to UncoreCache boundaries.
- **CDI Integration**: Manages CDI spec files to inject environment variables containing the allocated cpuset into the container.
- **State Synchronization**: On restart, the driver synchronizes with all existing pods on the node to rebuild its state of CPU allocations from environment variables injected by CDI.
- **Multiple Device Exposure Modes**:
  - **Individual Mode**: Each CPU is a device, allowing for selection based on attributes like CPU ID, core type, NUMA node, etc. This mode is ideal for workloads requiring fine-grained control over CPU placement, common in HPC or performance-critical applications.
  - **Grouped Mode**: CPUs are grouped (e.g., by NUMA node or socket) and treated as a consumable capacity within that group. This helps in reducing the number of devices exposed to the API server, especially on systems with a large number of CPUs, thus improving scalability. This mode is suitable for workloads needing alignment with other DRA resources within the same group (e.g., NUMA node) or where the exact CPU IDs are less critical than the quantity.

### Not Supported

- This driver currently only manages CPU resources. Memory allocation and management are not supported.
- While the driver is topology-aware, the grouped mode currently abstracts some of the fine-grained details within the group. Future enhancements may explore combining [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) with [partitionable devices](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/4815-dra-partitionable-devices/README.md) for more hierarchical control.

## Getting Started

### Installation

- If needed, create a kind cluster. We have one in the repo, if needed, that
  can be deplayed as follows:
  - `make kind-cluster`
- Deploy the driver and all necessary RBAC configurations using the provided
  manifest
  - `kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/dra-driver-cpu/refs/heads/main/install.yaml`

### Example Usage

The driver supports two modes of operation. Each mode has a complete example manifest that includes both the ResourceClaim(s) and a sample Pod. The ResourceClaim requests a specific number of exclusive CPUs from the driver, and is referenced in the Pod spec to receive the allocated CPUs.

#### Grouped Mode (default)

In grouped mode, CPUs are requested as a consumable capacity from a device group (e.g., NUMA node or socket). This example requests 10 CPUs.

- `kubectl apply -f hack/examples/pod_with_resource_claim_grouped_mode.yaml`

#### Individual Mode

In individual mode, specific CPU devices are requested by count, allowing for fine-grained control over CPU selection. This example includes two ResourceClaims requesting 4 and 6 CPUs respectively, used by a Pod with multiple containers.

- `kubectl apply -f hack/examples/pod_with_resource_claim_individual_mode.yaml`

## Example ResourceSlices

Here's how the `ResourceSlice` objects might look for the different modes:

### Individual Mode

Each CPU is listed as a separate device with detailed attributes.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: dra-driver-cpu-worker-dra.cpu-qskwf
  # ... other metadata
spec:
  driver: dra.cpu
  nodeName: dra-driver-cpu-worker
  pool:
    generation: 1
    name: dra-driver-cpu-worker
    resourceSliceCount: 1
  devices:
  - attributes:
      dra.cpu/cacheL3ID:
        int: 0
      dra.cpu/coreID:
        int: 1
      dra.cpu/coreType:
        string: standard
      dra.cpu/cpuID:
        int: 1
      dra.cpu/numaNodeID:
        int: 0
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
    name: cpudev0
  - attributes:
      dra.cpu/cacheL3ID:
        int: 0
      dra.cpu/coreID:
        int: 1
      dra.cpu/coreType:
        string: standard
      dra.cpu/cpuID:
        int: 33
      dra.cpu/numaNodeID:
        int: 0
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
    name: cpudev1
  # ... other CPU devices
```

### Grouped Mode (e.g., by NUMA node)

CPUs are grouped, and the device entry shows consumable capacity.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: dra-driver-cpu-worker-dra.cpu-tp869
  # ... other metadata
spec:
  driver: dra.cpu
  nodeName: dra-driver-cpu-worker
  pool:
    generation: 1
    name: dra-driver-cpu-worker
    resourceSliceCount: 1
  devices:
  - allowMultipleAllocations: true
    attributes:
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/numCPUs:
        int: 64
      dra.cpu/numaNodeID:
        int: 0
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
    capacity:
      dra.cpu/cpu:
        value: "64"
    name: cpudevnuma0
  - allowMultipleAllocations: true
    attributes:
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/numCPUs:
        int: 64
      dra.cpu/numaNodeID:
        int: 1
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 1
    capacity:
      dra.cpu/cpu:
        value: "64"
    name: cpudevnuma1
```

### Running the tests

To run the e2e tests against a custom-setup, special-purpose kind cluster, just run

```bash
make test-e2e-kind
```

Set the environment variable `DRACPU_E2E_VERBOSE` to 1 for more output:

```bash
DRACPU_E2E_VERBOSE=1 make test-e2e-kind
```

In some cases, you may need to set explicitly the KUBECONFIG path:

```bash
KUBECONFIG=${HOME}/.kube/config DRACPU_E2E_VERBOSE=1 make test-e2e-kind
```

The `test-e2e-kind` will exercises the same flows which are run on the project CI.
The full documentation for all the supported environment variables is found in the [tests README](test/e2e/README.md).

**NOTE** the custom-setup kind cluster is _not_ automatically tear down once the tests terminate
**NOTE** if you want to run again the tests, just use `make test-e2e`. Please see `make help` for more details.

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).
Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/) - preferred channels: #sig-node #wg-device-management
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)
