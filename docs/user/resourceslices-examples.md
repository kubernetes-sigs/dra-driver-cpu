# Example ResourceSlices

Here's how the `ResourceSlice` objects might look for the different modes:

## Individual Mode

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

## Grouped Mode (e.g., by NUMA node)

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

## With Node allocatable mapping

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
