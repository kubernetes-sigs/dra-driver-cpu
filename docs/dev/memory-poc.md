# Memory integration POC boundaries

The [runbook](../user/memory-poc.md) contains setup and execution instructions.
The intent and implementation checklist are in the repository's `PLAN.md`.

The existing driver identity, CPU allocation store, and ordinary CPU-only
preparation remain in place. New discovery code represents anonymous memory as
consumable bytes per resource type and NUMA node. Static capacities exclude all
configured hugetlb bytes from ordinary memory; free-memory snapshots are not
advertised as DRA capacity. Separate pools are published in one driver update.
The resource-type discriminator prevents the CPU DeviceClass from matching
memory devices. Health reports retain the pool identity of each device.

Memory-containing and multi-request claims use companion records keyed by claim
UID, with assignments and owners per selected request. Their aggregate CPU
reservation remains in the CPU store. This avoids changing that store into a
generic resource manager. CPU result allocation reuses the existing policy.
Preparation reserves CPUs before CDI I/O and rolls back on failure.

One CDI spec contains all request devices for a composed claim. Each result
returns only its request's device ID. Versioned `DRA_RESOURCE_*` JSON payloads
carry the selected assignment to NRI. New payloads are validated against prepared
records; the existing `DRA_CPUSET_*` CPU path remains supported. Do not set either
reserved prefix in workload environments.

NRI unions selected CPU and memory-node sets and sums selected byte quantities.
It applies container regular-memory and hugetlb limits because the pinned
Kubernetes checkout's native-resource status aggregates per claim. Unselected
hugetlb allocations from mixed claims are cleared without erasing allocations
selected from other claims. Memory-only containers remain tracked as shared-CPU
containers. Sharing one request across containers is rejected; disjoint requests
from one claim are permitted. Unprepare, not container stop, releases prepared
state. Synchronization uses candidate state and reconstructs running composed
claims from their driver-owned CDI specs; this adds no durable-state guarantee.

`--enable-memory` is an operational switch outside versioned configuration
schemas. Memory-enabled startup requires native mappings and documented feature
gates. The chart exposes the switch as top-level `enableMemory`.

The host-run harness uses Go setup/preflight code copied from the reference
repository at d6a6c17, with attribution in each copied file. It has no dependency
on that repository. `bin/pocsetup` is built locally. The copied runtime helper
handles containerd config v2/v3; the copied preflight validator checks mount and
superblock options. Extra kind checks cover controller delegation, ancestor
2 MiB hugetlb budgets, page inventory, and runtime readiness. 1 GiB capacity
is optional for setup and is selected or skipped per e2e case; the probe tolerates
an absent 1 GiB controller file unless a 1 GiB limit is expected. Native mappings replace
the reference driver's need to write pod-level hugetlb cgroups directly.

The setup code alone uses `go-toml/v2` to preserve containerd TOML and
`moby/sys/mountinfo` for thread-local cgroup mount discovery. These are necessary,
maintained parsers, not a dependency on the memory driver. Setup does not run
privileged init containers or give the driver new host access. Existing CPU-only deployment/CI paths explicitly disable the memory family.
Release build and CI Kubernetes versions are unchanged; a temporary workspace supplies
staging modules for the pinned development checkout.
