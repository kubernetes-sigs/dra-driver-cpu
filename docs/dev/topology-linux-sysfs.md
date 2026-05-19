# Deep dive: how linux reports topology attributes

disclosure: this document was created with AI assistance.
The research was human-steered by LLM-operated; the document was then reviewed by humans
for correctness and edited extensively.

This document assumes kernel code, architecture and hardware management knowledge.

This document tries to be architecture agnostic whenever possible, but the author can only claim sufficient
knowledge about the `x86_64` architecture.
The reader must assume the "silicon" and hardware is assumed to be `x86_64` unless clearly specified otherwise.

## Key assumptions and key omissions

- Bare-metal x86 servers (roughly 2020+ generation).
- Linux kernel behavior as in version 7.0.9.
- ACPI-enabled systems with valid and correct ACPI tables, from which topology information is drawn (SRAT / `_PXM`)
- Production(-like) environments (no manual sysfs overrides, no debugging workarounds).
- Topology profile fixed per boot (for example NPS/SNC mode chosen in BIOS and kept stable).

Intentionally ignored:

- NUMA Interleave aka `NPS=0`. See section 4 for details.
- Manual writes to `/sys/bus/pci/devices/*/numa_node`.
- Firmware-bug fallback behavior and platform quirks (e.g. workarounding bad ACPI tables).
- Virtualized/paravirtualized PCI NUMA assignment paths, creating topologies with no correspondence to real silicon.
  IOW, the document addresses software which wants to run directly on bare metal, mediated by the host OS, not upper
  in the stack (e.g. virtualized guests).

## Definitions

- `pcieRoot`: root-complex identifier (`pci<domain>:<bus>`) derived from sysfs path topology
  by `GetPCIeRootAttributeByPCIBusID()` in `k8s.io/dynamic-resource-allocation/deviceattribute`.
- `numaNode`: integer from `/sys/bus/pci/devices/<BDF>/numa_node`.
- `local_cpulist`: cpulist from `/sys/bus/pci/devices/<BDF>/local_cpulist`.

### 1. How topology data, most notably `local_cpulist` and `numa_node`, is produced

In this section we first do a 10k-feet-high review of how the Linux kernel
exports the `numa_node` and the `local_cpulist` attributes and how these relate to each other.
These sysfs fields are especially important because they are pivotal to consume the
PCIE Root or NUMA node attributes in the CPU DRA driver.

#### 1.1 The sysfs entry point

The sysfs file `/sys/bus/pci/devices/<BDF>/local_cpulist` is implemented in
`drivers/pci/pci-sysfs.c:127`. It calls the shared helper `pci_dev_show_local_cpu()`
(line 104) with `list=true`. The sibling file `local_cpus` calls the same helper with
`list=false` (line 120).

```c
// drivers/pci/pci-sysfs.c:104-118
static ssize_t pci_dev_show_local_cpu(struct device *dev, bool list,
                                      struct device_attribute *attr, char *buf)
{
    const struct cpumask *mask;

#ifdef CONFIG_NUMA
    if (dev_to_node(dev) == NUMA_NO_NODE)
        mask = cpu_online_mask;
    else
        mask = cpumask_of_node(dev_to_node(dev));
#else
    mask = cpumask_of_pcibus(to_pci_dev(dev)->bus);
#endif
    return cpumap_print_to_pagebuf(list, buf, mask);
}
```

#### 1.2 `dev_to_node()` returns `dev->numa_node`

```c
// include/linux/device.h:844-848
#ifdef CONFIG_NUMA
static inline int dev_to_node(struct device *dev)
{
    return dev->numa_node;
}
```

#### 1.3 `cpumask_of_node()` returns ALL CPUs on the NUMA node

On x86 (`arch/x86/mm/numa.c:417-435`):

```c
const struct cpumask *cpumask_of_node(int node)
{
    // ... bounds checks ...
    return node_to_cpumask_map[node];
}
```

Therefore, for this path, `local_cpulist` is not an independent hardware signal.
It is computed from the device NUMA node.
There is no per-device or per-bus CPU affinity finer than the NUMA node.

#### 1.4 How PCI devices get their NUMA node

Executive summary:

- root complex has one ACPI proximity-domain mapping,
- bus node is derived from that mapping,
- each PCI device node inherits the NUMA node from bus.

Breakdown:

During PCI device enumeration (`drivers/pci/probe.c:2736`):

```c
set_dev_node(&dev->dev, pcibus_to_node(bus));
```

Every PCI device inherits its NUMA node from its bus. The bus, in turn, gets its
NUMA node from the host bridge (root complex) during root bridge setup.

#### 1.5 How the PCI bus gets its NUMA node (x86 ACPI path)

On x86, `pci_acpi_scan_root()` (`arch/x86/pci/acpi.c:533-577`) creates the root bus
with its sysdata:

```c
struct pci_bus *pci_acpi_scan_root(struct acpi_pci_root *root)
{
    int node = pci_acpi_root_get_node(root);
    ...
    struct pci_sysdata sd = {
        .domain = domain,
        .node = node,
        .companion = root->device
    };
```

`pci_acpi_root_get_node()` (`arch/x86/pci/acpi.c:451-467`) calls:

```c
static int pci_acpi_root_get_node(struct acpi_pci_root *root)
{
    int busnum = root->secondary.start;
    struct acpi_device *device = root->device;
    int node = acpi_get_node(device->handle);
    ...
}
```

#### 1.6 `acpi_get_node()` evaluates `_PXM` on the host bridge

```c
// drivers/acpi/numa/srat.c:692-699
int acpi_get_node(acpi_handle handle)
{
    int pxm;
    pxm = acpi_get_pxm(handle);
    return pxm_to_node(pxm);
}
```

`acpi_get_pxm()` (`drivers/acpi/numa/srat.c:675-690`) walks up the ACPI namespace
from the given handle, evaluating the `_PXM` (Proximity Domain) method at each level
until one returns a value:

```c
static int acpi_get_pxm(acpi_handle h)
{
    unsigned long long pxm;
    acpi_status status;
    acpi_handle handle;
    acpi_handle phandle = h;

    do {
        handle = phandle;
        status = acpi_evaluate_integer(handle, "_PXM", NULL, &pxm);
        if (ACPI_SUCCESS(status))
            return pxm;
        status = acpi_get_parent(handle, &phandle);
    } while (ACPI_SUCCESS(status));
    return -1;
}
```

The proximity domain is then mapped to a Linux NUMA node via `pxm_to_node()`,
using the PXM-to-node mapping built from the SRAT (System Resource Affinity Table)
during early boot.

#### 1.7 The `pci_sysdata` structure (x86)

```c
// arch/x86/include/asm/pci.h:14-29
struct pci_sysdata {
    int     domain;     /* PCI domain */
    int     node;       /* NUMA node */
    ...
};
```

`pcibus_to_node()` on x86 reads `pci_sysdata->node` (`arch/x86/include/asm/pci.h:110-113`):

```c
static inline int __pcibus_to_node(const struct pci_bus *bus)
{
    return to_pci_sysdata(bus)->node;
}
```

#### 1.8 Summary of the data flow

```
ACPI host bridge (root complex)
    |
    +-- identity: pci<domain>:<bus>  -------------->  pcieRoot (DRA attribute)
    |
    `-- _PXM method -- acpi_get_pxm() -> pxm_to_node()
            |
            `-- pci_sysdata.node --> pcibus_to_node()
                    |
                    +-- set_dev_node(&dev->dev, ...) --->  /sys/.../numa_node
                    |                                           |
                    `-- cpumask_of_node(node) --------->  /sys/.../local_cpulist
```

Both `pcieRoot` and `numaNode` originate from the same ACPI host bridge object.
One is the bridge's identity; the other is an attribute (`_PXM`) evaluated on
that same bridge.

### 2. `pcieRoot` and `numaNode` are related attributes

The kernel source review demonstrate that knowing `pcieRoot` fully determines `numaNode`
on any given machine.

Recap:

1. `pcieRoot` identifies the ACPI PCI host bridge (root complex). DRA computes it by
   resolving the sysfs symlink at `/sys/bus/pci/devices/<BDF>` to find the
   `pci<domain>:<bus>` directory under `/sys/devices/`
   (see `resolvePCIeRoot()` in `staging/.../deviceattribute/pci_linux.go`).

1. `numaNode` is the NUMA node assigned to that same host bridge, derived from `_PXM`
   evaluated on the bridge's ACPI device handle (`arch/x86/pci/acpi.c:455`,
   `drivers/acpi/numa/srat.c:692-699`).

1. Each host bridge has exactly one `_PXM` value, therefore exactly one NUMA node.
   All devices behind that bridge inherit it (`drivers/pci/probe.c:2736`).

1. Multiple host bridges can share the same NUMA node (e.g., NPS=1 on AMD EPYC:
   all root complexes on a socket share one NUMA node). But a host bridge can never
   span multiple NUMA nodes.

Therefore:

- `numaNode = F(pcieRoot)` - a many-to-one function.
- Same `pcieRoot` always implies same `numaNode`.
- Same `numaNode` does NOT imply same `pcieRoot` (multiple roots can share a node).
- `pcieRoot` is strictly finer-grained than `numaNode`.

### 3. What `numaNode` provides over `pcieRoot`

`numaNode` still provides a coarser grouping convenience. Specifically,
it answers: "which root complexes share a NUMA domain?" without requiring the user to
know the platform-specific mapping from root complex to NUMA node.

With `pcieRoot` alone, a user who wants "GPU and NIC on the same NUMA node but
possibly different root complexes" would need out-of-band knowledge of which root
complexes share a NUMA domain.

Note this is still an approximation. There's arguably a logical jump caused by
insufficient information and the grouping is coarse. We have no means to actually
learn and consume the information about which pcieRoot is closer to which other
pcieRoot, or L3/LLC (Last Level Cache) complex or any group of resource related
to the CPU microtopology.

Still, we could use `numaNode` as "next upper" allocation boundary above the `pcieRoot`
boundary.

### 4. NUMA Nodes granularity and its dependencies on firmware and hardware configuration

Important preamble: modern `x86_64` silicon absorbed the memory controller chip in the CPU package.
High end CPUs may have more than a memory controller, but the reverse (memory controller out of the CPUs,
connection between memory controller and CPU cores using the Front Side Bus - FSB) is no longer true in modern silicon.

Therefore, for the production-style systems in scope here (NUMA node interleaving disabled - see below, sane firmware),
the working assumption is that a CPU socket can host 1 or more NUMA nodes.
This is an operational assumption, not a Linux invariant.

Quoting the Linux kernel documentation (`Documentation/mm/numa.rst`):

```
Linux divides the system's hardware resources into multiple software
abstractions called "nodes". Linux maps the nodes onto the physical cells
of the hardware platform, abstracting away some of the details for some
architectures. As with physical cells, software nodes may contain 0 or more
CPUs, memory and/or IO buses.
```

So, while socket-local NUMA mappings are common on modern x86 servers, socket-to-node
relationships should not be treated as universally fixed across all firmware modes.

With that in mind, let's take into account AMD EPYC systems. It's important however to remark these
considerations are **not** AMD-specific. Intel processors have related but different considerations
relative to NUMA representation.
With that said: on AMD EPYC systems with NPS=1 (the common default), the entire socket is a single
NUMA node.
All root complexes on that socket share `numaNode=0` (or whatever single node the socket maps to).
In this configuration:

- `matchAttribute` on `numaNode` correctly reflects the topology the firmware
  exposes, but since all devices on the socket share a single NUMA node, it
  cannot distinguish placement within the socket.
- `matchAttribute` on `pcieRoot` provides finer discrimination in this case,
  since root complexes remain distinct even when they share a NUMA node.
- Both attributes report accurate data; the difference is that `pcieRoot`
  retains granularity that the NPS=1 configuration collapses for `numaNode`.

#### 4.1. NPS/SNC > 1 - sub-NUMA split

When the firmware is configured with `NPS>1` (or equivalent `SNC` mode), it exposes more NUMA nodes per socket to Linux.
Compared to `NPS=1`, this increases NUMA-node granularity seen by software.

For PCI devices, the kernel path does not change: each device still inherits `numa_node` from its bus/root-bridge association,
and `local_cpulist` is still derived from `cpumask_of_node(numa_node)`.
However, the node map can be finer, so devices that previously shared one node may now appear on different nodes.

#### 4.2. Acknowledging NUMA node interleaving (`NPS=0`)

Some modern systems offer a non-default firmware mode that presents a UMA-like software
topology (single NUMA node across sockets), even though the silicon is physically NUMA.
This mode is usually named `NUMA Node Interleaving` or `NPS=0`.

This mode invalidates one core assumption used elsewhere in this document: that a single
Linux NUMA node does not span sockets. That assumption can still hold at physical-topology
level, but it is no longer what the OS and applications observe.

Platform guidance generally frames this mode as a compatibility option for workloads that
are not NUMA-aware. It is usually not recommended for modern performance-sensitive workloads,
which typically benefit from explicit NUMA exposure and locality-aware placement.

For this reason, the rest of this document assumes standard NUMA firmware mode
(interleaving disabled / `NPS!=0`), where Linux observes per-socket or finer NUMA-node
topology.

#### 4.3. CXL and other topologies

The most common scenario encountered with modern high-end silicon is symmetric NUMA nodes
containing CPUs, memory controllers, and PCIe roots.
This is not a hard rule. Technologies like CXL (Compute Express Link) and some SoCs,
most notably nvidia Grace Hopper may produce different "irregular" topologies like
memory-only nodes, nodes without PCIe interconnect or any variation thereof.

Existence of irregular/asymmetric topologies must therefore be taken into account,
and the assumption that topologies are symmetric, like in many modern `x86_64` systems,
should always be challenged.

### 5. Linux Kernel gaps

#### 5.1 What the kernel knows about CPU topology

The kernel exposes fine-grained CPU-to-CPU topology via sysfs
(`/sys/devices/system/cpu/cpuN/topology/`), driven by CPUID (x86) or PPTT (ARM):

| Attribute      | Granularity                     | Source                            |
| -------------- | ------------------------------- | --------------------------------- |
| `package_cpus` | Physical package (often socket) | Topology enumeration (x86: CPUID) |
| `die_cpus`     | Die (sometimes chiplet-aligned) | Topology enumeration (x86: CPUID) |
| `cluster_cpus` | Cluster (x86: L2-sharing group) | Topology enumeration (x86: CPUID) |
| `core_cpus`    | SMT siblings (same core)        | Topology enumeration (x86: CPUID) |

These are defined in `drivers/base/topology.c:84-106` and the x86 topology accessors
in `arch/x86/include/asm/topology.h:143-198`.
In Linux 7.0.9 on x86, `cluster_cpus` is backed by `topology_cluster_cpumask()`,
which maps to `cpu_clustergroup_mask()` and then `cpu_l2c_shared_mask()`.

#### 5.2 What the kernel knows about PCI device placement

| Attribute       | Granularity   | Source                       |
| --------------- | ------------- | ---------------------------- |
| `numa_node`     | NUMA node     | ACPI `_PXM` on host bridge   |
| `local_cpulist` | Same as above | `cpumask_of_node(numa_node)` |

#### 5.3 The gap explained

There is no kernel interface that connects a PCI device to a specific die, chiplet,
L3 domain, or I/O die quadrant within a NUMA node. The PCI subsystem stops at
NUMA-node granularity. The CPU topology subsystem knows about sub-NUMA structure but
has no concept of I/O devices.

This gap exists because Linux's generic PCI-device locality interface is at NUMA-node level
(`numa_node`, `local_cpulist`). ACPI and architecture-specific code can describe richer
CPU/cache/memory locality, but there is no standardized kernel interface that maps a PCI
device to die/chiplet/LLC domains within a NUMA node.
Very notably, internal interconnect details are vendor-specific and change across
product generations.
