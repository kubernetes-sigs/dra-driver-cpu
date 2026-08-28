# Feature Support

## Currently Supported

- **Exclusive CPU Allocation**: Pods that request CPUs via a ResourceClaim are allocated exclusive CPUs based on the chosen mode and topology.
- **Shared CPU Pool Management**: All other containers without a ResourceClaim are confined to a shared pool of CPUs that are not reserved.
- **Topology Awareness**: The driver discovers detailed CPU topology including sockets, NUMA nodes, cores, SMT siblings, L3 cache (UncoreCache), and core types (Performance/Efficiency).
- **Advanced CPU Allocation Strategies**: When in `"grouped"` mode, the driver utilizes allocation logic adapted from the Kubelet's CPU Manager, including:
  - NUMA aware best-fit allocation.
  - Packing or spreading CPUs across cores.
  - Preference for aligning allocations to UncoreCache boundaries.
- **Multiple Device Exposure Modes**: `individual` (one device per CPU, fine-grained
  attribute-based selection — ideal for HPC and performance-critical workloads) or `grouped`
  (NUMA/socket/machine aggregates exposed as consumable capacity — fewer API objects, scales
  to large systems). See [Configuration](configuration.md#driver-configuration) for the
  full description and how to choose.
- **Device Health Reporting**: The driver reports per-device health to the kubelet via the DRA `WatchHealthStatus` gRPC API, reflected in `pod.status.containerStatuses[].allocatedResourcesStatus`. Devices are reported `Healthy` and go `Unknown` if the driver stops sending updates. `Unhealthy` is reserved for future use. See [Device Health Reporting](workload-requirements.md#device-health-reporting).

## Not Supported

- This driver currently only manages CPU resources. Memory allocation and management are not supported.
- While the driver is topology-aware, the grouped mode currently abstracts some of the fine-grained details within the group. Future enhancements may explore combining [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) with [partitionable devices](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/4815-dra-partitionable-devices/README.md) for more hierarchical control.

### Sharing resource claims

This driver strictly enforces a 1-to-1 mapping between Claims and Containers.
It does not support sharing a single ResourceClaim among multiple containers or multiple pods,
if that claims includes a resource (`dra.cpu`) managed by this driver.
Attempting to share a claim among containers or pods will make all but the first pod consuming
the claim to fail to start with the error `CreateContainerError` and remain in `Pending` state.
When the driver runs with `publishNodeAllocatableResourceMapping`, sharing across pods is
rejected earlier by `kube-scheduler`: the second pod stays unschedulable with the message
`node allocatable resource claim ... has a mapped device and cannot be shared across pods`.

The rationale to disallow sharing is that sharing claim confuses resource accounting, which
is currently fragile because the lack of integration between the classic resource accounting
and DRA-managed core resources.

This gap is meant to be addressed by [KEP-5517 (Node Allocatable Resources)](https://github.com/kubernetes/enhancements/issues/5517).
However, until that KEP progresses and gets traction, the safest approach for this driver is to
prevent any resource claim sharing.

## Matching CPU Manager Options

The kubelet cpumanager supports [options](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#cpu-policy-static--options) to fine-tune the CPU allocation behavior.
This DRA driver aims to implement feature parity with the kubelet cpumanager. The following table summarizes how you can achieve a cpumanager functionality controlled by a cpumanager policy option.
Reference: [kubernetes 1.35.0](https://github.com/kubernetes/kubernetes/blob/v1.35.0/pkg/kubelet/cm/cpumanager/policy_options.go).

| CPU Manager Option        | Maturity | Kubelet development status | Driver equivalent functionality                                        | notes                 |
| ------------------------- | -------- | -------------------------- | ---------------------------------------------------------------------- | --------------------- |
| AlignBySocket             | alpha    | inactive                   | `cpuDeviceMode: grouped` + `groupBy: socket` config options            |                       |
| DistributeCPUsAcrossCores | alpha    | inactive                   | none yet; postponed till k8s feature graduates to beta                 |                       |
| DistributeCPUsAcrossNUMA  | beta     | active                     | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/46 | see below for details |
| PreferAlignByUnCoreCache  | beta     | active                     | builtin; enabled by default                                            |                       |
| FullPCPUsOnly             | GA       | N/A                        | see issue: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/45 |                       |
| StrictCPUReservation      | GA       | N/A                        | builtin; enabled by default                                            |                       |

### Distributing CPUs across NUMA nodes

It is currently possible to encode a split of CPUs in such a way the allocator picks them from different NUMA nodes. Example:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: claim-cpu-capacity-20
spec:
  devices:
    requests:
    - name: cpus-spread-numa
      exactly:
        deviceClassName: dra.cpu
        count: 2
        capacity:
          requests:
            dra.cpu/cpu: "10"
    constraints:
    - requests: ["cpus-spread-numa"]
      distinctAttribute: resource.kubernetes.io/numaNode
```

However, this is only a partial replacement of the corresponding CPU Manager
option. The main problem is that a single `count` > 1 request only works for
equal-sized slices. In the above example, we artificially split the real
20-CPU request into two 10-CPU results, and the math must be done manually.
This also ties the spread to the machine topology: the same claim sent to a
machine with 2 NUMA nodes would spread as intended, on a machine with 4 NUMA
nodes it would still use only two NUMA nodes because `count: 2` asks for
exactly two distinct devices, and on a machine with 1 NUMA node it would fail
because there is no second NUMA node to choose. If you need 15 CPUs, this
exact single-request pattern cannot express a 7+8 split; you would need
multiple requests with different capacities.

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
(see [the topology deep dive](../dev/topology-linux-sysfs.md) for in-depth research based on Linux kernel 7.0.9), so grouped allocation within a NUMA
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
      resource.kubernetes.io/numaNode:
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
      dra.cpu/numaNodeID:
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
