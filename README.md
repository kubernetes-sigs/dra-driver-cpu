# dra-driver-cpu

Kubernetes Dynamic Resource Allocation (DRA) driver for CPU resources.
This repository implements a DRA driver that enables Kubernetes clusters to manage and assign exclusive CPUs to workloads using the DRA framework.
This driver replaces the [CPUManager](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/) functionalities implemented in the kubelet.

## Configuration

**IMPORTANT:** The kubelet's CPUManager implements assignment of exclusive CPUs to workloads. The CPUManager and this DRA driver are mutually incompatible and only
one can be enabled at a time on any given node.

## Kubelet configuration prerequisites

You need to disable the CPUManager on the nodes you wish to run this DRA driver.

1. The default settings of the kubelet are compatible with this DRA driver. If you never fine-tuned the kubelet, you are probably fine.
1. Make sure `cpuManagerPolicy: "none"` is set in the kubelet [configuration file](https://kubernetes.io/docs/tasks/administer-cluster/kubelet-config-file/).
1. If you changed the kubelet configuration, restart the kubelet to take effect. **NOTE:** you may need to [delete the CPUManager state file](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#changing-the-cpu-manager-policy).
1. You may now proceed with deploying and configuring this DRA driver.

## Driver configuration

The driver can be configured with the following command-line flags:

- `--cpu-device-mode`: Sets the mode for exposing CPU devices.
  - `"individual"`: Exposes each allocatable CPU as a separate device in the `ResourceSlice`. This mode provides fine-grained control as it exposes granular information specific to each CPU as device attributes in the `ResourceSlice`.
  - `"grouped" (default)`: Exposes a single device representing a group of CPUs. This mode treats CPUs as a [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) within the group, improving scalability by reducing the number of API objects.
- `--group-by`: When `--cpu-device-mode` is set to `"grouped"`, this flag determines the grouping strategy.
  - `"numanode"` (default): Groups CPUs by NUMA node.
  - `"socket"`: Groups CPUs by socket.
- `--reserved-cpus`: Specifies a set of CPUs to be reserved for system and kubelet processes. These CPUs will not be allocatable by the DRA driver and would be excluded from the `ResourceSlice`. The value is a cpuset, e.g., `0-1`. This semantic is the same as the one the kubelet applies with its `static` CPU Manager policy and enabling [`strict-cpu-reservation`](https://kubernetes.io/blog/2024/12/16/cpumanager-strict-cpu-reservation/) flag and specifying the CPUs with the [`reservedSystemCPUs`](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#explicitly-reserved-cpu-list) to be reserved for system daemons. For correct CPU accounting, the number of CPUs reserved with this flag should match the sum of the kubelet's `kubeReserved` and `systemReserved` settings. This ensures the kubelet subtracts the correct number of CPUs from `Node.Status.Allocatable`.
- `--disable-node-allocatable-mapping`: Disables the native NodeAllocatable resource mapping in the `ResourceSlice` (per [KEP-5517: DRA: Node Allocatable Resources](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5517-dra-node-allocatable-resources/README.md), tracked in [enhancements issue #5517](https://github.com/kubernetes/enhancements/issues/5517)).
  - **How it works**: By default (`false`), the driver automatically adds a native resource mapping entry to the exposed CPU devices in its `ResourceSlice` objects. This allows the `kube-scheduler` to perform **Unified Accounting** across standard resources and DRA resources (`DynamicResources` plugin). The scheduler natively subtracts the CPU capacity allocated to DRA `ResourceClaims` directly from the Node's total Allocatable CPU capacity. This avoids resource overcommit/double-counting when running a mix of DRA and non-DRA workloads on the node, and removes the need to duplicate resource requests in the standard container `resources.requests.cpu` block.
  - **When to disable**: Set this flag to `true` if you are relying on the duplicate/mirror resource declaration workarounds in the Pod spec to perform scheduler accounting manually (as described in [With KEP-5517 Disabled](#with-kep-5517-disabled-or-on-older-kubernetes-control-planes-131)), or if your Kubernetes control plane is older than 1.37 and does not support KEP-5517.
- `--mixed-allocation-mode`: Enables hybrid "mixed allocation" mode for workloads.
  - **How it works**: When enabled, a single container can simultaneously request dedicated exclusive CPUs via DRA resource claims (for low-latency, performance-critical tasks) and standard shared CPU resources via `resources.requests.cpu` in the standard Pod spec (to run background helper threads, sidecars, or bursty standard tasks). The driver's NRI plugin dynamically detects this hybrid request by inspecting the container's runtime CGroup CPU shares (which Kubelet configures to `shares > MinCPUShares` only if the container has explicit standard CPU requests). It then adjusts the container's CPU affinity to the union of its dedicated exclusive CPUs and the general shared CPU pool, while dynamically shrinking the shared CPU pool of other containers by subtracting those exclusive CPUs. (Default: `false`).

## How it Works

The driver is deployed as a DaemonSet which contains two core components:

- **DRA driver**: This component is the main control loop and handles the interaction with the Kubernetes API server for Dynamic Resource Allocation.

  - **Topology Discovery**: It discovers the node's CPU topology, including details like sockets, NUMA nodes, cores, SMT siblings, Last-Level Cache (LLC), and core types (e.g., Performance-cores, Efficiency-cores). This is done by reading sysfs files.
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

  - For containers with **guaranteed CPUs** (those with a DRA ResourceClaim and no standard CPU requests), the plugin reads the environment variable injected via CDI and pins the container strictly to its exclusive CPU set using the cgroup cpuset controller.
  - For containers running in **mixed allocation mode** (those with both DRA ResourceClaims and standard CPU requests), the plugin detects this by inspecting the container's CGroup CPU shares (where Kubelet configures `shares > 2` when a container has explicit CPU requests). It pins the container to the union of its dedicated exclusive CPUs and the shared CPU pool.
  - For all other containers, it confines them to a **shared pool** of CPUs, which consists of all allocatable CPUs not exclusively assigned to any guaranteed container.
  - It dynamically updates the shared pool cpuset for all shared and mixed containers whenever exclusive allocations change (exclusive containers are created, modified, or removed).
  - On restart, the NRI plugin synchronizes its state by inspecting existing containers, their CGroup shares, and environment variables to fully rebuild CPU allocations.

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
- **NodeAllocatable Resource Mapping (KEP-5517)**: Enables natively mapping exposed CPU devices in the `ResourceSlice` to node-allocatable capacities. This allows the kube-scheduler to track and subtract DRA allocations natively, guaranteeing coordinated scheduling with standard CPU workloads on the same node without double-booking.
- **Mixed Allocation Mode**: Allows hybrid workloads where a single container can be allocated dedicated exclusive CPUs via DRA claims while simultaneously using the shared CPU pool for standard/bursty workloads, detected automatically via CGroup shares.

### Not Supported

- This driver currently only manages CPU resources. Memory allocation and management are not supported.
- While the driver is topology-aware, the grouped mode currently abstracts some of the fine-grained details within the group. Future enhancements may explore combining [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) with [partitionable devices](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/4815-dra-partitionable-devices/README.md) for more hierarchical control.

#### Sharing resource claims

This driver strictly enforces a 1-to-1 mapping between Claims and Containers.
It does not support sharing a single ResourceClaim among multiple containers or multiple pods,
if that claims includes a resource (`dra.cpu`) managed by this driver.
Attempting to share a claim among containers or pods will make all but the first pod consuming
the claim to fail to start with the error `CreateContainerError` and remain in `Pending` state.

The rationale to disallow sharing is that sharing claim confuses resource accounting, which
is currently fragile because the lack of integration between the classic resource accounting
and DRA-managed core resources.

This gap is meant to be addressed by KEP-5517 (Native Resource Management). However, until
that KEP progresses and gets traction, the safest approach for this driver is to prevent
any resource claim sharing.

### Mixed Allocation Mode

Mixed Allocation Mode allows a hybrid execution model where a single container can simultaneously access dedicated exclusive CPUs (allocated via DRA claims for low-latency tasks) and standard shared CPU capacity (for sidecars, helper threads, or background tasks).

#### When is Mixed Allocation Mode Enabled?

Mixed Allocation Mode is enabled for a workload only when both of the following conditions are met:

1. **Driver Flag**: The driver is run with the `--mixed-allocation-mode` command-line flag set to `true` (default: `false`).
1. **Container Spec**: The target container requests *both* standard CPU resources (via `resources.requests.cpu`) and a DRA CPU claim (via `resources.claims`).

> [!IMPORTANT]
> **Container CPU Shares**: Kubelet configures a container's runtime CPU cgroup shares strictly based on its standard `resources.requests.cpu` block. If standard CPU requests are omitted, Kubelet defaults the container's shares to `2`. The driver's NRI plugin relies on this behavior and only enables mixed mode for containers with shares strictly greater than `2` (i.e. when standard CPU requests exist). Therefore, you **must** specify standard CPU requests in the container spec alongside your claims to trigger mixed allocation.

##### Example Workload Snippet

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pod-cpu-mixed-mode
spec:
  containers:
    - name: main-application
      image: "registry.k8s.io/pause:3.9"
      resources:
        requests:
          cpu: "2" # Requests 2 shared CPUs for background tasks (triggers mixed mode)
        claims:
          - name: "dedicated-cpu-claim-ref" # Requests 4 exclusive CPUs
  resourceClaims:
    - name: "dedicated-cpu-claim-ref"
      resourceClaimName: claim-cpu-capacity-4 # Requests 4 CPUs
```

#### Falling Back to Guaranteed-Only Mode

If the driver's `--mixed-allocation-mode` flag is set to `true`, but the target container does *not* request standard CPU resources in its spec, the container **automatically defaults to strictly Guaranteed-only mode**.

Under this mode:

- The container is pinned **exclusively** to its assigned dedicated CPUs.
- The container is **denied access** to the node's general shared CPU pool.
- The CPU affinity mask set by the NRI plugin will contain *only* the exclusive cores.

##### Example Guaranteed-Only Snippet

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: pod-cpu-guaranteed-only
spec:
  containers:
    - name: high-perf-application
      image: "registry.k8s.io/pause:3.9"
      resources:
        # Omit standard requests.cpu (CPU shares will default to 2)
        claims:
          - name: "dedicated-cpu-claim-ref" # Requests 4 exclusive CPUs
  resourceClaims:
    - name: "dedicated-cpu-claim-ref"
      resourceClaimName: claim-cpu-capacity-4 # Requests 4 CPUs
```

#### Thread Pinning and Workload Responsibility

When Mixed Allocation Mode is active, the NRI plugin configures the container's overall CPU affinity mask to be the **union** of its dedicated exclusive CPUs and the node's shared CPU pool.

While this ensures the container can access both resource pools, please note:

- **OS Scheduler Behavior**: By default, the OS scheduler may run *any* thread in the container on *any* CPU in the union cpuset.
- **Workload Responsibility**: **It is the workload's responsibility** to programmatically pin its performance-critical, low-latency threads to the dedicated exclusive CPUs, leaving the shared pool for background or non-critical threads.

##### How to Pin Threads Programmatically

1. **Discover Exclusive CPUs**: The driver automatically injects environment variables starting with the `DRA_CPUSET_` prefix into the container (e.g., `DRA_CPUSET_<claim-uid>=0,1`). The value contains the specific exclusive cpuset assigned to that DRA claim.
1. **Set Thread Affinity**: Workloads should programmatically parse these environment variables within their application code and use standard OS system calls (such as `sched_setaffinity` on Linux) to bind high-priority processing threads directly to that exclusive cpuset.

### Matching CPU Manager functionality

The kubelet cpumanager support [options](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#cpu-policy-static--options) to fine-tune the CPU allocation behavior.
This DRA driver aims to implement feature parity with the kubelet cpumanager. The following table summarize how you can achieve a cpumananger functionality controlled by a cpumanager policy option.
Reference: [kubernetes 1.35.0](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/kubelet/cm/cpumanager/policy_options.go).

| CPU Manager Option        | Maturity | Kubelet development status | Driver equivalent functionality                                        | notes                 |
| ------------------------- | -------- | -------------------------- | ---------------------------------------------------------------------- | --------------------- |
| AlignBySocket             | alpha    | inactive                   | `--grouped-mode` driver option                                         |                       |
| DistributeCPUsAcrossCores | alpha    | inactive                   | none yet; postponed till k8s feature graduates to beta                 |                       |
| DistributeCPUsAcrossNUMA  | beta     | active                     | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/46 | see below for details |
| PreferAlignByUnCoreCache  | beta     | active                     | builtin; enabled by default                                            |                       |
| FullPCPUsOnly             | GA       | N/A                        | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/45 |                       |
| StrictCPUReservation      | GA       | N/A                        | builtin; enabled by default                                            |                       |

### Distributing CPUs across NUMA nodes

It is currently possible to do encode a split of CPUs in such a way the allocator picks them from different NUMA nodes. Example:

```
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: claim-cpu-capacity-20
spec:
  devices:
    requests:
    - name: numa0-cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "10"
        selectors:
        - cel:
            expression: device.attributes["dra.cpu"].numaNodeID == 0
    - name: numa1-cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "10"
        selectors:
        - cel:
            expression: device.attributes["dra.cpu"].numaNodeID ==1
```

However, this is only a partial replacement of the corresponding CPU Manager option. The main problem of this approach is that it leaks assumptions about machine properties.
We hardcode the NUMA split and, unlike the cpumanager feature, it won't automatically adapt if the same claim is handled by a 1-NUMA, 2-NUMA or 4-NUMA machine;
the claim would need to be updated or recreated manually.

## Workload Configuration Requirements

Currently, Kubernetes has two separate systems for requesting CPU resources: standard requests in pod/container fields (`pod.spec.resources` or `pod.spec.containers[].resources`) and DRA `ResourceClaim`s.

- The Kube-scheduler uses different plugins to account for these requests, and these plugins are mutually independent. This can lead to node CPU overcommitment because the scheduler might not have a complete picture of all allocated CPUs.

- Kubelet only considers the standard CPU requests in the PodSpec for critical node-level enforcements like [QoS class](https://kubernetes.io/docs/tasks/configure-pod-container/quality-service-pod/) assignment and cgroup hierarchy setup, ignoring CPUs allocated via DRA claims.

This discrepancy is a known issue being addressed by [KEP-5517: Native Resource Management for DRA](https://github.com/kubernetes/enhancements/issues/5517). Until KEP-5517 is implemented, you MUST configure your pods using one of the following methods to ensure correct behavior and resource accounting:

### With KEP-5517 Enabled (Default, Recommended)

When KEP-5517 is active (meaning `--disable-node-allocatable-mapping` is set to `false`), the `kube-scheduler` performs **Unified Accounting**. It automatically tracks and subtracts the CPU capacity allocated to DRA `ResourceClaims` directly from the node's total Allocatable CPU pool.

Consequently, you **DO NOT** need to duplicate or mirror resource requests in the container's standard `resources.requests.cpu` block. A container can request its DRA `ResourceClaim` directly, and scheduling accounting works out-of-the-box without risk of overcommitment.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app-with-dra-cpu
spec:
  containers:
    - name: workload-container
      image: "registry.k8s.io/pause:3.9"
      resources:
        claims:
          - name: "pod-cpu-claim-ref"
  resourceClaims:
    - name: "pod-cpu-claim-ref"
      resourceClaimName: claim-cpu-capacity-10 # Requests 10 CPUs
```

> [!NOTE]
> You should only include standard `resources.requests.cpu` alongside your claims if you are utilizing **Mixed Allocation Mode** (to leverage both dedicated and shared CPU pools) or if you need to define a custom QoS class/limits at the Pod level.

______________________________________________________________________

### With KEP-5517 Disabled (or on older Kubernetes control planes < 1.31)

If you explicitly set `--disable-node-allocatable-mapping=true` or are running on control planes that do not support KEP-5517, standard resources and DRA claims are processed in isolated accounting lanes. To prevent scheduler-level double-booking and node overcommitment, you **MUST** configure your workloads using one of the following duplicate/mirror resource declaration workarounds:

#### **Option A (Preferred): Pod Level Resources (`pod.spec.resources`)**

This approach is preferred as it clearly defines the pod's total CPU budget and works well for pods with a mix of containers, some needing exclusive CPUs (requested via DRA) and others using shared CPUs.

- Set `pod.spec.resources.requests.cpu` and `pod.spec.resources.limits.cpu` to the *sum* of all CPUs requested across all DRA claims used by containers in this pod, PLUS any additional CPUs for containers NOT using DRA claims.
- Containers using DRA claims may omit `cpu` from their `resources.requests` and `resources.limits`. The Pod Level Resources will govern the QoS class and set cgroup limits at the pod level.

```yaml
# Example: Pod Level Resources Workaround
spec:
  resources: # Pod Level Resources
    requests:
      cpu: "16" # 10 (exclusive cpu's for claim1) + 4 (exclusive cpu's for claim2) + 2 (shared cpus for sidecar1 and sidecar2)
    limits:
      cpu: "16"
  containers:
    - name: main-app
      image: ...
      resources:
        # Omit CPU requests/limits, or set both to 10
        claims:
          - name: claim1
    - name: worker
      image: ...
      resources:
       # Omit CPU requests/limits, or set both to 4
        claims:
          - name: claim2
    - name: sidecar1
      image: ...
      # Omit CPU resources, or ensure the combined requests/limits for sidecar1 and sidecar2 do not exceed 2.
    - name: sidecar2
      image: ...
      # Omit CPU resources, or ensure the combined requests/limits for sidecar1 and sidecar2 do not exceed 2.
  resourceClaims:
    - name: claim1
      resourceClaimName: cpu-claim-10 # Requests 10 CPUs
    - name: claim2
      resourceClaimName: cpu-claim-4  # Requests 4 CPUs
```

#### **Option B: Container-Level Resources (No Pod Level Resources)**

For each container that uses a DRA CPU claim, set `spec.containers[].resources.requests.cpu` and `spec.containers[].resources.limits.cpu` to be *exactly equal* to the number of CPUs requested in the `ResourceClaim` referenced by that container.

```yaml
# Example: Container Level Mirroring Workaround
spec:
  containers:
    - name: my-container
      image: ...
      resources:
        requests:
          cpu: "10" # Must match the CPU count in "claim1"
        limits:
          cpu: "10" # Must match the CPU count in "claim1"
        claims:
          - name: claim1
  resourceClaims:
    - name: claim1
      resourceClaimName: cpu-claim-10 # Requests 10 CPUs
```

**1-to-1 Claim to Container:** This driver enforces that a specific CPU `ResourceClaim` can only be used by *one* container within or across pods. See [Sharing resource claims](#sharing-resource-claims).

## Prerequisites

The driver relies on [NRI (Node Resource Interface)](https://github.com/containerd/nri) to pin containers to their
allocated CPUs, and on [CDI (Container Device Interface)](https://github.com/cncf-tags/container-device-interface) to
inject the allocated cpuset into the container environment.

### Minimum Runtime Requirements

Both NRI and CDI are enabled by default in modern container runtimes:

| Runtime    | NRI enabled by default | CDI enabled by default |
| ---------- | ---------------------- | ---------------------- |
| containerd | 2.0+                   | 2.0+                   |
| CRI-O      | 1.30+                  | always                 |

Both runtimes also ship with the following CDI spec directories configured by default:

```toml
cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

No manual runtime configuration is needed if you are running one of the versions above or newer.

### Manual Configuration for Older Runtimes

If you are running an older version of containerd (pre-2.0), you need to manually enable CDI and NRI in the containerd
configuration (typically `/etc/containerd/config.toml`) and restart containerd.

#### Enable CDI

```toml
[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

#### Enable NRI

```toml
[plugins."io.containerd.nri.v1.nri"]
  disable = false
  disable_connections = false
  plugin_config_path = "/etc/nri/conf.d"
  plugin_path = "/opt/nri/plugins"
  plugin_registration_timeout = "5s"
  plugin_request_timeout = "5s"
  socket_path = "/var/run/nri/nri.sock"
```

After editing the config, restart containerd:

```bash
systemctl restart containerd
```

## Getting Started

### Installation

If needed, create a kind cluster. We have one in the repo, if needed, that
can be deployed as follows:

```bash
make kind-cluster
```

The recommended way to install the driver is via the provided Helm chart:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system
```

See the [Helm chart README](deployment/helm/dra-driver-cpu/README.md) for the full list of configuration options.

#### Installation via install.yaml (deprecated)

> **Deprecated:** `install.yaml` is deprecated in favor of the Helm chart and will be removed in a future release.
> New users should use the Helm-based installation above.

```bash
make manifests
kubectl apply -f dist/install.yaml
```

### Migrating from install.yaml to Helm

Because the DaemonSet label selectors differ between `install.yaml` (`app: dracpu`) and the Helm chart
(`app.kubernetes.io/name`, `app.kubernetes.io/instance`), and DaemonSet selectors are immutable, an
in-place migration is not possible. The only practical migration path is a delete and reinstall:

```bash
# Step 1: remove the install.yaml-managed resources
kubectl delete -f dist/install.yaml

# Step 2: install the Helm-managed release
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system
```

**Disruption:** Deleting the DaemonSet terminates the driver pods on all nodes simultaneously. During
the migration window, no new CPU allocations can be made and the shared-pool cpuset updates stop.
Existing workloads are not evicted and their CPUs should remain. Once the new DaemonSet is scheduled
and the driver pods are running, the driver should recover its state.

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

## Developer Notes

These notes collect contributor-oriented commands and repository quirks.

### Testing Local Changes in a Kind Cluster

The simplest way to build the driver from source and deploy it into a new Kind cluster is using:

```bash
make ci-kind-setup
```

This command builds the driver image, creates a Kind cluster, loads the image, and installs the driver manifests in `grouped` mode (the default).

To install the driver in `individual` mode instead, use:

```bash
DRACPU_E2E_CPU_DEVICE_MODE=individual make ci-kind-setup
```

To clean up the environment when finished, run:

```bash
make delete-kind-cluster
```

### Linting

Run the linter against the codebase:

```bash
make lint
```

To automatically fix lint issues, pass `--fix` via the `GOLANGCI_LINT_EXTRA_ARGS` variable:

```bash
GOLANGCI_LINT_EXTRA_ARGS=--fix make lint
```

`GOLANGCI_LINT_EXTRA_ARGS` is forwarded verbatim to `golangci-lint run`, so any other supported flags can be passed the same way.

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).
Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/) - preferred channels: #sig-node #wg-device-management
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)
