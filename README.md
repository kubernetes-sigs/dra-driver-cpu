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
  - `"machine"`: Groups all allocatable node CPUs into a single machine-wide capacity device. **NOTE**: This mode requires an external scheduler to supply core assignments. See [Custom Opaque CPUSet Allocation Overrides](#custom-opaque-cpuset-allocation-overrides).
- `--reserved-cpus`: Specifies a set of CPUs to be reserved for system and kubelet processes. These CPUs will not be allocatable by the DRA driver and would be excluded from the `ResourceSlice`. The value is a cpuset, e.g., `0-1`. This semantic is the same as the one the kubelet applies with its `static` CPU Manager policy and enabling [`strict-cpu-reservation`](https://kubernetes.io/blog/2024/12/16/cpumanager-strict-cpu-reservation/) flag and specifying the CPUs with the [`reservedSystemCPUs`](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#explicitly-reserved-cpu-list) to be reserved for system daemons. For correct CPU accounting, the number of CPUs reserved with this flag should match the sum of the kubelet's `kubeReserved` and `systemReserved` settings. This ensures the kubelet subtracts the correct number of CPUs from `Node.Status.Allocatable`.
- `--expose-pcie-roots`: If enabled, adds the "resource.kubernetes.io/pcieRoot" standard value to CPU devices, to report the PCIe roots close to each device. Since it always reports values as list, this option requires the cluster Feature Gate `DRAListTypeAttributes` (see KEP 5491) to be enabled. The driver has no way to introspect the cluster Feature Gate, so care must be taken to enable first the Feature Gate then this option.

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
  - The `DRA_CPUSET_*` environment variable prefix is reserved for the driver. Containers with malformed `DRA_CPUSET_*` values are rejected during creation.
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

### Matching CPU Manager functionality

The kubelet cpumanager supports [options](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#cpu-policy-static--options) to fine-tune the CPU allocation behavior.
This DRA driver aims to implement feature parity with the kubelet cpumanager. The following table summarizes how you can achieve a cpumanager functionality controlled by a cpumanager policy option.
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

It is currently possible to encode a split of CPUs in such a way the allocator picks them from different NUMA nodes. Example:

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

### Exposing PCIe roots

The DRA CPU Driver can expose the PCIe root locality of CPU devices via the standard `resource.kubernetes.io/pcieRoot` attribute.
This feature is opt-in, and requires _both_ the `DRAListTypeAttributes` Feature Gate (see KEP-5491) enabled in the cluster and the `--expose-pcie-roots` command line
flag in the driver. The driver has no way to introspect the cluster feature gate states, so care must be taken to keep the configuration consistent.

**IMPORTANT NOTE**: it is recommended to consume the `pcieRoot` list attributes using the `matchAttribute` or [the derived attributes](https://github.com/kubernetes/enhancements/issues/6080).
Care must be taken to consume the attribute using the CEL expressions selector, because the backward compatibility path is not yet clear
(see: https://github.com/kubernetes/enhancements/pull/6081#issuecomment-4606653735 and following)

#### Current limitations (v0.2.0)

In grouped mode, the `pcieRoot` attribute reports the union of all PCIe roots local to the group's allocatable CPUs.
When `matchAttribute` is used for cross-driver co-location (e.g., CPU + NIC), the scheduler matches on a shared root,
but the driver's CPU allocator selects CPUs within the socket/NUMA group _without taking into account the exact matched root_.
The consequence is that `pcieRoot` in grouped mode should be read as "the group contains CPUs associated with these roots",
not "the allocated CPUs are guaranteed to be local to the selected root".

In practice, this distinction is currently not harmful because the kernel's PCIe bus CPU affinity collapses to NUMA-node granularity
(see docs/dev/topology-linux-sysfs.md for in-depth research based on Linux kernel 7.0.9), so grouped allocation within a NUMA
node inherently stays within a single root's affinity domain.

For future releases, we plan to both introduce means to feed the driver with finer-grained PCIe root locality and to implement
PCIe-root-aware CPU selection in the core allocator.

#### Implementation details

While devices don't expose the PCIe root locality, the reverse is true: the linux kernel does report the CPUs local to PCIe buses and devices; the driver scans the PCIe
buses and tracks the PCIe host bridges CPU locality; from there, we can reconstruct the CPU to PCIe root mapping and then populate the attributes.

This is an example of a resource slice produced by a driver running in a kind CI cluster, grouped mode, grouping by numa nodes:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  creationTimestamp: "2026-05-29T14:09:35Z"
  generateName: 00000-dra.cpu-dra-driver-cpu-worker-
  generation: 1
  name: 00000-dra.cpu-dra-driver-cpu-worker-v7pdl
  ownerReferences:
  - apiVersion: v1
    controller: true
    kind: Node
    name: dra-driver-cpu-worker
    uid: 80fbb23c-ae26-44b4-a21a-dce4037db82d
  resourceVersion: "651"
  uid: 08664794-f96b-43fd-b8ce-233c7bd172f6
spec:
  devices:
  - allowMultipleAllocations: true
    attributes:
      dra.cpu/numCPUs:
        int: 31
      dra.cpu/numaNodeID:
        int: 0
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:00
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
    capacity:
      dra.cpu/cpu:
        value: "31"
    name: cpudevnuma000
  driver: dra.cpu
  nodeName: dra-driver-cpu-worker
  pool:
    generation: 1
    name: dra-driver-cpu-worker
    resourceSliceCount: 1
```

Note the amount of PCIe roots may vary and depends on both the physical wiring of the system and on whether slots are populated or not;
most firmware don't enumerate PCIe buses - and therefore don't expose PCIe roots - if no devices are connected.

## Workload Configuration Requirements

Currently, Kubernetes has two separate systems for requesting CPU resources: standard requests in pod/container fields (`pod.spec.resources` or `pod.spec.containers[].resources`) and DRA `ResourceClaim`s.

- The Kube-scheduler uses different plugins to account for these requests, and these plugins are mutually independent. This can lead to node CPU overcommitment because the scheduler might not have a complete picture of all allocated CPUs.

- Kubelet only considers the standard CPU requests in the PodSpec for critical node-level enforcements like [QoS class](https://kubernetes.io/docs/tasks/configure-pod-container/quality-service-pod/) assignment and cgroup hierarchy setup, ignoring CPUs allocated via DRA claims.

This discrepancy is a known issue being addressed by [KEP-5517: Native Resource Management for DRA](https://github.com/kubernetes/enhancements/issues/5517). Until KEP-5517 is implemented, you MUST configure your pods using one of the following methods to ensure correct behavior and resource accounting:

- **Option A (Preferred): Pod Level Resources (`pod.spec.resources`)**

  - This approach is generally preferred as it more clearly defines the pod's total CPU budget and works well for pods with a mix of containers, some needing exclusive CPUs (requested via DRA) and others using shared CPUs.
  - Set `pod.spec.resources.requests.cpu` and `pod.spec.resources.limits.cpu` to the *sum* of all CPUs requested across all DRA claims used by containers in this pod, PLUS any additional CPUs for containers NOT using DRA claims.
  - Containers using DRA claims may omit `cpu` from their `resources.requests` and `resources.limits`. The Pod Level Resources will govern the QoS class and set cgroup limits at the pod level.

  ```yaml
  # Example: Pod Level Resources
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

- **Option B: Container-Level Resources (No Pod Level Resources)**

  - For each container that uses a DRA CPU claim, set `spec.containers[].resources.requests.cpu` and `spec.containers[].resources.limits.cpu` to be *exactly equal* to the number of CPUs requested in the `ResourceClaim` referenced by that container.

  ```yaml
  # Example: Container Level Mirroring
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

## Custom Opaque CPUSet Allocation Overrides

When using `grouped` device mode with the `--group-by=machine` configuration, the DRA driver does not perform automatic topology-aware CPU allocation. Instead, an explicit core assignment must be provided via the `cpuset` field in the claim's opaque configuration parameters.

The Kubelet driver parses this configuration at prepare time from the claim's allocation status (`status.allocation.devices.config`). The control plane (typically scheduling plugin) is responsible for injecting this configuration block into the allocation result when binding the claim.

### Opaque Parameters Schema (`v1alpha1`)

The `opaque.parameters` field must conform to the following schema:

| Field              | Type   | Description                                                                                |
| :----------------- | :----- | :----------------------------------------------------------------------------------------- |
| `apiVersion`       | string | Must be set to `v1alpha1`.                                                                 |
| `cpuConfig`        | object | Container object for CPU configurations.                                                   |
| `cpuConfig.cpuset` | string | Specifies the list of CPU cores in standard Linux cpuset format (e.g. `"2-5"`, `"0,4-6"`). |

### Example of a Fully Allocated ResourceClaim with Opaque Configuration

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: exclusive-cores-claim
  namespace: default
  uid: claim-uid-123456
spec:
  devices:
    requests:
    - name: cpu-request-1
      exactly:
        deviceClassName: dra.cpu
        count: 4
status:
  allocation:
    devices:
      results:
      - request: cpu-request-1
        driver: dra.cpu
        pool: test-node
        device: cpudevmachine
        consumedCapacity:
          dra.cpu/cpu: "4"
      config:  # Added by external scheduler
      - source: FromClaim
        requests:
        - cpu-request-1
        opaque:
          driver: dra.cpu
          parameters:
            apiVersion: v1alpha1
            cpuConfig:
              cpuset: "2-5"
```

The driver allocates the specified cores directly (after validating that they are allocatable and not reserved). If it is omitted, resource preparation will fail and pod startup will be rejected.

> [!IMPORTANT]
> **Validation Rules:**
>
> - The `cpuset` value must be specified in standard Linux cpuset format representing ranges and/or individual cores (e.g. `"2-5"`, `"1-10,15"`).
> - **Per-Request Configurations Only**: In `status.allocation.devices.config[]`, every configuration block's `requests` array must map to exactly one request in the claim. We do not support multiple claim requests mapping to the same cpuset config.
> - **ResourceClaim Source**: Opaque configurations must originate from the `ResourceClaim`. Configurations defined in DeviceClass (`spec.config[]`) are not supported and will result in a validation error. Since a configuration in the `DeviceClass` applies to all claims referencing it, configuring a `cpuset` there would assign the same CPUs to multiple claims, failing allocation due to conflict between claims.
> - **CPUSet Validation**: The driver verifies that the custom cpuset is valid for the host machine and is currently allocatable. It checks that:
>   - The cores are part of the node's online CPUs.
>   - The cores are not reserved using the driver's `--reserved-cpus` configuration flag.
> - **Error Handling**: If validation fails (e.g. core conflict, size mismatch, duplicate target, or offline cores), the driver returns a failure immediately in Kubelet's `PrepareResourceClaims` hook, causing pod startup to fail.

## Metrics

The driver exposes Prometheus metrics on the existing HTTP `/metrics` endpoint served by `--bind-address` (default `:8080`).

> [!NOTE]
> Driver custom metrics are ALPHA. They are useful for early observability, but metric names, labels, buckets, and semantics may change in future releases.

Custom driver metrics can also be listed programmatically without starting the driver:

```bash
dracpu --show-metrics
```

The command prints JSON metadata for custom `dra_cpu_*` metrics only. It does not include default Go runtime, process, or Prometheus client metrics.

| Metric                                   | Type      | Labels   | Description                                                                            |
| ---------------------------------------- | --------- | -------- | -------------------------------------------------------------------------------------- |
| `dra_cpu_allocated_cpus`                 | Gauge     | none     | CPUs currently allocated to prepared resource claims.                                  |
| `dra_cpu_available_cpus`                 | Gauge     | none     | CPUs still available for allocation after reserved and active claim CPUs are excluded. |
| `dra_cpu_reserved_cpus`                  | Gauge     | none     | CPUs excluded from DRA management by driver configuration.                             |
| `dra_cpu_resource_claims_active`         | Gauge     | none     | Resource claims currently recorded as active by the allocation store.                  |
| `dra_cpu_prepare_claims_total`           | Counter   | `result` | Per-claim `PrepareResourceClaims` results. `result` is `success`, `error`, or `unknown`. |
| `dra_cpu_unprepare_claims_total`         | Counter   | `result` | Per-claim `UnprepareResourceClaims` results. `result` is `success`, `error`, or `unknown`. |
| `dra_cpu_prepare_claim_duration_seconds` | Histogram | none     | Per-claim prepare latency in seconds.                                                  |
| `dra_cpu_claim_allocated_cpus`           | Histogram | none     | CPUs allocated for each newly successful claim allocation.                             |

The custom metrics intentionally avoid labels for namespace, pod, claim, device, node, socket, NUMA node, group mode, and error reason. Those labels would either be high-cardinality or need more API design before becoming part of the driver's metric surface. Node identity should come from scrape target labels.

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
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system

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
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
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
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
      # Only populated if the driver is run with --expose-pcie-roots=true
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:00
    name: cpudev000
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
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
      # Only populated if the driver is run with --expose-pcie-roots=true
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:00
    name: cpudev001
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
      # Only populated if the driver is run with --expose-pcie-roots=true
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:00
        - pci0000:10
    capacity:
      dra.cpu/cpu:
        value: "64"
    name: cpudevnuma000
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
      # Only populated if the driver is run with --expose-pcie-roots=true
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:40
        - pci0000:50
    capacity:
      dra.cpu/cpu:
        value: "64"
    name: cpudevnuma001
```

### Running the tests

#### Unit Tests

To run the node-local unit tests, run:

```bash
make test-unit
```

#### E2E Tests

To run the e2e tests against a custom-setup, special-purpose kind cluster, just run:

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

The `test-e2e-kind` will exercise the same flows which are run on the project CI.
The full documentation for all the supported environment variables is found in the [tests README](test/e2e/README.md).

**NOTE:** the custom-setup kind cluster is _not_ automatically torn down once the tests terminate.
**NOTE:** if you want to run the tests again on the existing kind cluster, just use `make test-e2e`. Please see `make help` for more details.

## Troubleshooting & Diagnostics

The repository includes a diagnostic tool `dracpu-gatherinfo` to collect node-local CPU topology and driver configuration information. This is helpful for debugging CPU allocation issues or validating that the host topology is discovered correctly.

For detailed usage instructions, see the [dracpu-gatherinfo documentation](docs/gatherinfo.md).

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
