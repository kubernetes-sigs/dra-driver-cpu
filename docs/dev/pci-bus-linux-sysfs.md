# Deep dive: finding and exploring PCI/PCIe root buses

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

## Summary

After analyzing the user-visible sysfs pseudo filesystem layout from both userspace and kernel source code,
there are two reliable ways to identify root buses in linux:

1. **Resolve each symlink in `/sys/class/pci_bus`** and check whether the path between `devices/` and `pci_bus/`
   contains only the `pciDDDD:BB` root directory (root bus) or also contains intermediate BDF device components (child bus).
1. **Enumerate `/sys/devices/pci*/`** directly — every directory matching that glob is a root complex, by construction.

While both methods are equivalent, the method 2 avoids symlink resolution, which can lead to simpler implementation
and/or data wrapping.

## How Root Buses Are Created

Root buses are registered exclusively through `pci_register_host_bridge()`.

**`drivers/pci/probe.c:987-1061`** — the relevant sequence:

```
line 1000:  bus = pci_alloc_bus(NULL);                       // NULL parent = root bus
line 1028:  dev_set_name(&bridge->dev, "pci%04x:%02x", ...)  // creates "pci0000:XX"
line 1037:  device_add(&bridge->dev);                        // registers /sys/devices/pci0000:XX/
line 1041:  bus->bridge = get_device(&bridge->dev);
line 1052:  bus->dev.class = &pcibus_class;                  // associates with /sys/class/pci_bus/
line 1053:  bus->dev.parent = bus->bridge;                   // parent = the bridge device
line 1055:  dev_set_name(&bus->dev, "%04x:%02x", ...)        // bus device named "0000:XX"
line 1058:  device_register(&bus->dev);                      // registers the bus class device
```

The call at line 1028 is the **only place** in the entire kernel that creates the `pci%04x:%02x` device name pattern
(verification: grep in `drivers/pci/`).
Therefore: every directory under `/sys/devices/` matching `pci[0-9a-f]*:[0-9a-f]*` is a root complex created by this function.

The bus class device (line 1058) gets registered with `pcibus_class`, which creates the `/sys/class/pci_bus/0000:XX` symlink.

Since `bus->dev.parent = bus->bridge` (line 1053), and `bus->bridge` is the host bridge device at `/sys/devices/pci0000:XX/`,
the class symlink target path resolves to:

```
../../devices/pci0000:XX/pci_bus/0000:XX
```

## How Secondary Buses Are Created

Secondary (`child` in linux kernel parlance) buses are created through `pci_alloc_child_bus()`.

**`drivers/pci/probe.c:1200-1269`** — the relevant sequence:

```
line 1209:  child = pci_alloc_bus(parent);               // non-NULL parent = child bus
line 1213:  child->parent = parent;
line 1214:  child->sysdata = parent->sysdata;            // inherits sysdata (including NUMA node)
line 1227:  child->dev.class = &pcibus_class;
line 1228:  dev_set_name(&child->dev, "%04x:%02x", ...)  // same naming scheme
line 1242:  child->dev.parent = child->bridge;           // parent = the PCI-to-PCI bridge *device*
line 1265:  device_register(&child->dev);
```

The critical difference is at line 1242: `child->bridge` points to the **PCI-to-PCI bridge device**
(a `pci_dev` with a BDF address like `0000:00:0e.0`), not the host bridge.
This device sits within the parent bus's sysfs directory. So the class symlink resolves to a deeper path:

```
../../devices/pci0000:00/0000:00:0e.0/pci_bus/0000:03
                         ^^^^^^^^^^^^
                         bridge device BDF component
```

For multi-level topologies, each intermediate bridge adds another BDF segment:

```
../../devices/pci0000:00/0000:00:0e.0/0000:03:00.0/pci_bus/0000:04
                         ^^^^^^^^^^^^  ^^^^^^^^^^^^
                         1st bridge    2nd bridge
```
