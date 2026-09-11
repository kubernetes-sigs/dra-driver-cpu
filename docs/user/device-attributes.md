# Device Attributes and Selectors

Every CPU device the driver publishes carries topology attributes that claims can select on
with [CEL expressions](https://kubernetes.io/docs/reference/using-api/cel/).
This page is the attribute reference, with worked selector examples and sample
`ResourceSlice` objects for each device mode.

> [!NOTE]
> Attribute names and semantics are not yet a stable API: they may still change between
> driver minor releases while the project is pre-1.0.

## Attribute reference

Which attributes a device carries depends on the driver's device mode
(`cpuDeviceMode` in [Configuration](configuration.md)): `grouped` exposes one device per
CPU group, `individual` one device per CPU.

> [!IMPORTANT]
> SMT reporting currently supports only systems without SMT and systems with two logical
> CPUs per physical core. Do not rely on `dra.cpu/smtLevel` or `dra.cpu/smtMapV1` on
> systems which do not meet this constraint.

### Grouped mode (default)

#### Supported attributes

| Attribute                         | Type    | Description                                                                                                    |
| --------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------- |
| `resource.kubernetes.io/numaNode` | int     | Standard NUMA node of the group; published only when grouping by NUMA node                                     |
| `resource.kubernetes.io/pcieRoot` | strings | PCIe roots local to the group's CPUs; needs `--expose-pcie-roots` and the `DRAListTypeAttributes` feature gate |
| `dra.cpu/socketID`                | int     | CPU socket of the group; published when grouping by socket or NUMA node                                        |
| `dra.cpu/numCPUs`                 | int     | Number of allocatable CPUs in the group                                                                        |
| `dra.cpu/smtLevel`                | int     | Logical CPUs per core: currently `1` without SMT and `2` with SMT                                              |

When `allocator: external` is configured, grouped devices additionally expose attributes
to help external allocators to reserve resources efficiently:

| Attribute          | Type   | Description                                                                                                              |
| ------------------ | ------ | ------------------------------------------------------------------------------------------------------------------------ |
| `dra.cpu/cpuIDs`   | string | Linux cpuset representation of the allocatable logical CPUs in this group                                                |
| `dra.cpu/smtMapV1` | string | Version 1 encoding of the node-wide logical-CPU sibling relationships; see [Versioned attributes](#versioned-attributes) |

For example, an external allocator might receive the following additional attributes for a
group containing CPUs 0 through 7, whose sibling offset is 4:

```yaml
dra.cpu/cpuIDs:
  string: 0-7
dra.cpu/smtMapV1:
  string: 0-7>4
```

#### Compatibility / legacy attributes

These attributes are retained for compatibility with existing consumers and potential
cross-driver alignment. New consumers should use the supported attributes above instead.
These attributes will be removed in a future release.

| Attribute            | Type | Replaced by                       | Description                                                                                   |
| -------------------- | ---- | --------------------------------- | --------------------------------------------------------------------------------------------- |
| `dra.cpu/smtEnabled` | bool | `dra.cpu/smtLevel`                | Whether SMT/hyper-threading is enabled                                                        |
| `dra.cpu/numaNodeID` | int  | `resource.kubernetes.io/numaNode` | Driver-specific NUMA node; published only when grouping by NUMA node                          |
| `dra.net/numaNode`   | int  | `resource.kubernetes.io/numaNode` | Experimental cross-driver NUMA-alignment attribute; published only when grouping by NUMA node |

### Individual mode

#### Supported attributes

| Attribute                         | Type    | Description                                                                                           |
| --------------------------------- | ------- | ----------------------------------------------------------------------------------------------------- |
| `resource.kubernetes.io/numaNode` | int     | Standard NUMA node                                                                                    |
| `resource.kubernetes.io/pcieRoot` | strings | PCIe roots local to the CPU; needs `--expose-pcie-roots` and the `DRAListTypeAttributes` feature gate |
| `dra.cpu/cpuID`                   | int     | Logical CPU ID                                                                                        |
| `dra.cpu/coreID`                  | int     | Physical core ID, shared by SMT siblings                                                              |
| `dra.cpu/coreType`                | string  | `standard`, `p-core`, or `e-core`                                                                     |
| `dra.cpu/cacheL3ID`               | int     | L3 (last-level/uncore) cache group                                                                    |
| `dra.cpu/socketID`                | int     | CPU socket                                                                                            |
| `dra.cpu/smtLevel`                | int     | Logical CPUs per core: currently `1` without SMT and `2` with SMT                                     |

#### Compatibility / legacy attributes

These attributes are retained for compatibility. New consumers should use the supported
attributes above instead. They may be removed in a future release.

| Attribute            | Type | Replaced by                       | Description                                                                                   |
| -------------------- | ---- | --------------------------------- | --------------------------------------------------------------------------------------------- |
| `dra.cpu/smtEnabled` | bool | `dra.cpu/smtLevel`                | Whether SMT/hyper-threading is enabled                                                        |
| `dra.cpu/numaNodeID` | int  | `resource.kubernetes.io/numaNode` | Driver-specific NUMA node; published only when grouping by NUMA node                          |
| `dra.net/numaNode`   | int  | `resource.kubernetes.io/numaNode` | Experimental cross-driver NUMA-alignment attribute; published only when grouping by NUMA node |

### Prepared-device metadata

When a grouped device is prepared, the driver copies its published attributes into the device
metadata exposed to the container. It also adds the following metadata-only attribute when the
claim consumes `dra.cpu/cpu` capacity:

| Attribute                  | Type | Description                                                    |
| -------------------------- | ---- | -------------------------------------------------------------- |
| `dra.cpu/allocatedNumCPUs` | int  | Number of CPUs allocated to this claim from the grouped device |

This attribute is not published in a `ResourceSlice` and cannot be used to select a device.

## Versioned attributes

Some attributes contain a compact, driver-defined encoding instead of a Kubernetes-standard
value. We cannot promise that a encoding introduced by this driver is correct and complete on its
first release, or that it will not need a bug fix or extension.
We therefore version those attribute names, giving consumers a basis for a smooth upgrade instead of forcing
every consumer to change at once.
In particular, `dra.cpu/smtMapV1` is the first version of the SMT sibling-map encoding.

The suffix is a compact semantic version. Trailing `.0` components are omitted:

| Attribute-name suffix | Semantic version |
| --------------------- | ---------------- |
| `V1`                  | `v1.0.0`         |
| `V2_1`                | `v2.1.0`         |
| `V3_1_2`              | `v3.1.2`         |

The DRA attribute identifier permits letters, digits, and `_`, but not `.` or `-`. Therefore the
periods in the conceptual versions `V2.1` and `V3.1.2` are represented by underscores in the
actual attribute names: `dra.cpu/smtMapV2_1` and `dra.cpu/smtMapV3_1_2`.

When introducing an incompatible encoding, the driver can publish both the old and new attributes
(for example, `dra.cpu/smtMapV1` and `dra.cpu/smtMapV2`) for a migration period. Consumers should
select the highest version that they understand and ignore newer versions. Once published, the
meaning of a versioned attribute is immutable.

## Example ResourceSlices

Here's how the `ResourceSlice` objects might look for each mode.

### Grouped mode (default; grouping by NUMA node)

CPUs are grouped, and the device entry shows consumable capacity.

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceSlice
metadata:
  name: 00000-dra.cpu-dra-driver-cpu-worker-tp869
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
      dra.cpu/smtLevel:
        int: 2
      dra.cpu/numCPUs:
        int: 64
      resource.kubernetes.io/numaNode:
        int: 0
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
      dra.cpu/numaNodeID:
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
      dra.cpu/smtLevel:
        int: 2
      dra.cpu/numCPUs:
        int: 64
      resource.kubernetes.io/numaNode:
        int: 1
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 1
      dra.cpu/numaNodeID:
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

### Individual mode

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
      resource.kubernetes.io/numaNode:
        int: 0
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/smtLevel:
        int: 2
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
      dra.cpu/numaNodeID:
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
      resource.kubernetes.io/numaNode:
        int: 0
      dra.cpu/smtEnabled:
        bool: true
      dra.cpu/smtLevel:
        int: 2
      dra.cpu/socketID:
        int: 0
      dra.net/numaNode:
        int: 0
      dra.cpu/numaNodeID:
        int: 0
      # Only populated if the driver is run with --expose-pcie-roots=true
      resource.kubernetes.io/pcieRoot:
        strings:
        - pci0000:00
    name: cpudev001
  # ... other CPU devices
```

### With node allocatable mapping

When the driver runs with `publishNodeAllocatableResourceMapping: true` (requires the
`DRANodeAllocatableResources` feature gate, alpha in 1.37+), every device additionally
carries a `nodeAllocatableResources` entry translating its DRA allocation into node
allocatable `cpu`.

Grouped mode maps the consumed `dra.cpu/cpu` capacity 1:1:

```yaml
  devices:
  - allowMultipleAllocations: true
    capacity:
      dra.cpu/cpu:
        value: "64"
    name: cpudevnuma000
    nodeAllocatableResources:
      cpu:
        mapping:
          capacityKey: dra.cpu/cpu
          capacityMultiplier: "1"
```

Individual mode maps each device to one CPU:

```yaml
  devices:
  - name: cpudev000
    nodeAllocatableResources:
      cpu:
        mapping:
          deviceMultiplier: "1"
```

## Selecting CPUs based on properties with CEL

A selector is a [CEL](https://kubernetes.io/docs/reference/using-api/cel/) expression over
the attributes above; the scheduler only allocates devices for which every selector is true.

In the default `grouped` mode, CPUs are requested as `dra.cpu/cpu` capacity from a group
device, and selectors pick the group. A complete claim for 8 CPUs from NUMA node 0:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpus-on-numa0
spec:
  devices:
    requests:
    - name: cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "8"
        selectors:
        - cel:
            expression: device.attributes["resource.kubernetes.io"].numaNode == 0
```

In `individual` mode, each CPU is its own device, so claims request a `count` of devices and
selectors pick individual CPUs. A complete claim for 4 performance cores:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: performance-cores
spec:
  devices:
    requests:
    - name: cpus
      exactly:
        deviceClassName: dra.cpu
        count: 4
        selectors:
        - cel:
            expression: device.attributes["dra.cpu"].coreType == "p-core"
```

Any supported attribute works the same way — for example, use
`device.attributes["dra.cpu"].smtLevel == 1` to avoid nodes with SMT/hyper-threading enabled
(for example, for side-channel isolation).

Selectors filter each request independently; to make *multiple* requests land on matching
topology, add a
[`matchAttribute`](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
constraint. A complete claim requesting two CPU sets that must share a socket:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: same-socket-cpus
spec:
  devices:
    requests:
    - name: cpus-a
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "4"
    - name: cpus-b
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "4"
    constraints:
    - requests: ["cpus-a", "cpus-b"]
      matchAttribute: dra.cpu/socketID
```

The inverse is `distinctAttribute`: every request must get a *different* value. A complete
claim spreading two CPU sets across two different NUMA nodes — without hardcoding which
nodes, so the same claim works on any machine:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: numa-spread-cpus
spec:
  devices:
    requests:
    - name: cpus-a
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "8"
    - name: cpus-b
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "8"
    constraints:
    - requests: ["cpus-a", "cpus-b"]
      distinctAttribute: resource.kubernetes.io/numaNode
```

`distinctAttribute` is gated by `DRAConsumableCapacity` — the same feature gate the default
`grouped` mode uses, enabled by default from Kubernetes 1.36.

Similarly, for equal-sized slices you can use a single request with `count` > 1.
This repeats the same per-result capacity request multiple times. By itself,
`count` does not guarantee spreading, but combined with `distinctAttribute` it
can force the scheduler to place those results on different devices:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: numa-spread-cpus
spec:
  devices:
    requests:
    - name: cpus-multi
      exactly:
        deviceClassName: dra.cpu
        count: 2
        capacity:
          requests:
            dra.cpu/cpu: "8"
    constraints:
    - requests: ["cpus-multi"]
      distinctAttribute: resource.kubernetes.io/numaNode
```

For a longer discussion of the trade-offs of using `count` +
`distinctAttribute` for NUMA spreading, see
[Feature Support](feature-support.md#distributing-cpus-across-numa-nodes).

**NOTE**: An important point to stress is the role of the `distinctAttribute` constraint.
In grouped mode, all the exposed devices support multiple allocations. Therefore,
without the constraint, the scheduler can pick the same device multiple times
to fulfil the allocation request, if that device has enough remaining capacity.
In turn, the driver supports this request shape and will honor the request,
because it is valid and legal.
