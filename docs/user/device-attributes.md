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

### Grouped mode (default)

| Attribute                         | Type    | Description                                                                                                    |
| --------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------- |
| `dra.cpu/numaNodeID`              | int     | NUMA node of the group (published when grouping by NUMA node)                                                  |
| `dra.cpu/socketID`                | int     | CPU socket of the group (published when grouping by NUMA node or socket)                                       |
| `dra.cpu/numCPUs`                 | int     | CPUs available in the group                                                                                    |
| `dra.cpu/smtEnabled`              | bool    | Whether SMT/hyper-threading is enabled on the node                                                             |
| `dra.net/numaNode`                | int     | Cross-driver NUMA alignment, shared with e.g. NIC drivers (NUMA grouping)                                      |
| `resource.kubernetes.io/pcieRoot` | strings | PCIe roots local to the group's CPUs; needs `--expose-pcie-roots` and the `DRAListTypeAttributes` feature gate |

Grouped devices also expose the consumable capacity `dra.cpu/cpu` — the number of CPUs
claimable from the group. With `groupBy: machine`, only `numCPUs`, `smtEnabled`, and — when
`--expose-pcie-roots` is enabled — `resource.kubernetes.io/pcieRoot` are published.

### Individual mode

| Attribute                         | Type    | Description                                                                                           |
| --------------------------------- | ------- | ----------------------------------------------------------------------------------------------------- |
| `dra.cpu/cpuID`                   | int     | Logical CPU ID                                                                                        |
| `dra.cpu/coreID`                  | int     | Physical core ID (shared by SMT siblings)                                                             |
| `dra.cpu/coreType`                | string  | `standard`, `p-core`, or `e-core`                                                                     |
| `dra.cpu/cacheL3ID`               | int     | L3 (last-level/uncore) cache group                                                                    |
| `dra.cpu/numaNodeID`              | int     | NUMA node                                                                                             |
| `dra.cpu/socketID`                | int     | CPU socket                                                                                            |
| `dra.cpu/smtEnabled`              | bool    | Whether SMT/hyper-threading is enabled on the node                                                    |
| `dra.net/numaNode`                | int     | Cross-driver NUMA alignment, shared with e.g. NIC drivers                                             |
| `resource.kubernetes.io/pcieRoot` | strings | PCIe roots local to the CPU; needs `--expose-pcie-roots` and the `DRAListTypeAttributes` feature gate |

`resource.kubernetes.io/pcieRoot` is intended for cross-driver co-location via
`matchAttribute` — see [Feature Support](feature-support.md#exposing-pcie-roots) for details
and current limitations.

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
            expression: device.attributes["dra.cpu"].numaNodeID == 0
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

Any attribute works the same way — for example, swap the expression for
`device.attributes["dra.cpu"].smtEnabled == false` to avoid nodes with SMT/hyper-threading
enabled (e.g. for side-channel isolation).

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
      distinctAttribute: dra.cpu/numaNodeID
```

`distinctAttribute` is gated by `DRAConsumableCapacity` — the same feature gate the default
`grouped` mode uses, enabled by default from Kubernetes 1.36.

A complete claim splitting CPUs across two *specific* NUMA nodes with selectors is in
[Feature Support](feature-support.md#distributing-cpus-across-numa-nodes).
