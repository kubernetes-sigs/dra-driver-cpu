# Feature Support

## Currently Supported

- **Exclusive CPU Allocation**: Pods that request CPUs via a ResourceClaim are allocated exclusive CPUs based on the chosen mode and topology.
- **Shared CPU Pool Management**: All other containers without a ResourceClaim are confined to a shared pool of CPUs that are not reserved.
- **Topology Awareness**: The driver discovers detailed CPU topology including sockets, NUMA nodes, cores, SMT siblings, L3 cache (UncoreCache), and core types (Performance/Efficiency).
- **Advanced CPU Allocation Strategies**: When in `"grouped"` mode, the driver utilizes allocation logic adapted from the Kubelet's CPU Manager, including:
  - NUMA aware best-fit allocation.
  - Packing or spreading CPUs across cores.
  - Preference for aligning allocations to UncoreCache boundaries.
- **Multiple Device Exposure Modes**:
  - **Individual Mode**: Each CPU is a device, allowing for selection based on attributes like CPU ID, core type, NUMA node, etc. This mode is ideal for workloads requiring fine-grained control over CPU placement, common in HPC or performance-critical applications.
  - **Grouped Mode**: CPUs are grouped (e.g., by NUMA node or socket) and treated as a consumable capacity within that group. This helps in reducing the number of devices exposed to the API server, especially on systems with a large number of CPUs, thus improving scalability. This mode is suitable for workloads needing alignment with other DRA resources within the same group (e.g., NUMA node) or where the exact CPU IDs are less critical than the quantity.
- **Device Health Reporting**: The driver reports per-device health (Healthy/Unhealthy/Unknown) to the kubelet via the DRA `WatchHealthStatus` gRPC API, reflected in `pod.status.containerStatuses[].allocatedResourcesStatus`. See [Device Health Reporting](workload-requirements.md#device-health-reporting).

## Not Supported

- This driver currently only manages CPU resources. Memory allocation and management are not supported.
- While the driver is topology-aware, the grouped mode currently abstracts some of the fine-grained details within the group. Future enhancements may explore combining [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) with [partitionable devices](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/4815-dra-partitionable-devices/README.md) for more hierarchical control.

### Sharing resource claims

This driver strictly enforces a 1-to-1 mapping between Claims and Containers.
It does not support sharing a single ResourceClaim among multiple containers or multiple pods,
if that claims includes a resource (`dra.cpu`) managed by this driver.
Attempting to share a claim among containers or pods will make all but the first pod consuming
the claim to fail to start with the error `CreateContainerError` and remain in `Pending` state.

The rationale to disallow sharing is that sharing claim confuses resource accounting, which
is currently fragile because the lack of integration between the classic resource accounting
and DRA-managed core resources.

This gap is meant to be addressed by [KEP-5517 (Node Allocatable Resources)](https://github.com/kubernetes/enhancements/issues/5517).
However, until that KEP progresses and gets traction, the safest approach for this driver is to
prevent any resource claim sharing.

## CPU Manager Options Mapping

See [Matching CPU Manager Options](cpu-manager-mapping.md) for a comparison of kubelet cpumanager
policy options and their driver equivalents, including how to distribute CPUs across NUMA nodes.

## Exposing PCIe roots

The DRA CPU Driver can expose the PCIe root locality of CPU devices via the standard `resource.kubernetes.io/pcieRoot` attribute.
This feature is opt-in, and requires _both_ the `DRAListTypeAttributes` Feature Gate (see KEP-5491) enabled in the cluster and the `--expose-pcie-roots` command line
flag in the driver. The driver has no way to introspect the cluster feature gate states, so care must be taken to keep the configuration consistent.

**IMPORTANT NOTE**: it is recommended to consume the `pcieRoot` list attributes using the `matchAttribute` or [the derived attributes](https://github.com/kubernetes/enhancements/issues/6080).
Care must be taken to consume the attribute using the CEL expressions selector, because the backward compatibility path is not yet clear
(see: https://github.com/kubernetes/enhancements/pull/6081#issuecomment-4606653735 and following)

### Current limitations (v0.2.0)

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

### Implementation details

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
