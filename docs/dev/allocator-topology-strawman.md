# Strawman: CPU Topology Model and Allocator Boundary

This is an input document for discussion, not a final design. It is motivated
by the asymmetric-topology fix in
[PR #210](https://github.com/kubernetes-sigs/dra-driver-cpu/pull/210), the
long-running topology discussion in
[#35](https://github.com/kubernetes-sigs/dra-driver-cpu/issues/35), and the
need to consume finer-grained locality data in
[#173](https://github.com/kubernetes-sigs/dra-driver-cpu/issues/173).

## Summary

The driver currently has three concerns that are coupled more tightly than we
should rely on for long-term development:

1. CPU topology discovery from Linux sysfs.
1. CPU allocation policy, currently derived from kubelet CPUManager code.
1. ResourceSlice/device projection, which is the scheduler-visible API surface.

PR #210 is a scoped correctness fix: it keeps the current allocator and
ResourceSlice shape, but stops using symmetric averages such as
`CPUsPerCore`, `CPUsPerSocket`, and `CPUsPerUncore` in places where exact
`CPUDetails`/`CoreKey` queries are available.

That is the right short-term fix, but it also shows that `cpu_assignment.go`
has crossed the trivial kubelet-resync boundary. We should make that boundary
explicit and decide what we want to own.

My strawman recommendation is:

- Do not invent a new generic topology model first.
- Use Linux sysfs as the canonical discovery source on Linux nodes.
- Use hwloc as a reference model and possible future backend, not as an
  immediate hard dependency.
- Evolve the existing `pkg/cpuinfo` snapshot instead of introducing a parallel
  topology model.
- Split topology discovery, allocator queries, allocator policy, and
  ResourceSlice projection into explicit internal boundaries.
- Keep the current CPUManager-derived allocator as the baseline policy while
  adding room for DRA-specific allocation logic where the kernel and DRA API
  surface actually provide enough locality information.
- Treat ResourceSlice shape compatibility as a separate API decision, not as
  an accidental consequence of allocator internals.
- Keep Node Allocatable Resources and broader CPU accounting as an adjacent but
  separate design track.

## Background

### What PR #210 fixes

Issue #35 identified that the current `CPUTopology` helpers can infer topology
properties from global counts. That breaks when topology is not symmetric:
CPUs may be manually offlined, SMT may be disabled unevenly, core IDs may
repeat across sockets, or the platform may expose heterogeneous CPU/cache
structure.

PR #210 takes the smallest useful step:

- `CoreID` is no longer treated as globally unique where core-level precision
  matters. `CoreKey{SocketID, ClusterID, CoreID}` is used instead.
- Full socket/core/uncore checks compare against exact CPU sets from
  `CPUDetails` instead of averages.
- Partial uncore allocation picks enough exact core keys to satisfy the CPU
  request, then trims to the requested CPU count.
- The ResourceSlice model and the grouped/individual device modes are left
  unchanged.

This preserves the current behavior where possible and fixes the asymmetric
case without forcing a broader resource model redesign.

### Why this matters beyond PR #210

The current grouped allocator is derived from kubelet CPUManager. That code is
valuable because it is battle-tested and because we could previously resync it
with mostly mechanical changes. Once we need local changes in the allocator
itself, the value of "borrowed code with near-zero review cost" decreases.

This does not mean forking is bad. It means the project should consciously
decide where the fork starts, which invariants we keep from kubelet, and which
new invariants are DRA-specific.

Issue #173 makes this practical rather than theoretical. The driver can expose
PCIe root locality, but grouped allocation currently selects CPUs inside a
socket/NUMA group without consuming the exact matched PCIe root. Improving this
requires a clearer allocator/query boundary.

At the same time, the current repository research already shows an important
limit: Linux exposes generic PCI locality at NUMA-node granularity
(`numa_node`, `local_cpulist`), but it does not expose a standard mapping from
PCI devices to die, chiplet, or LLC domains inside a NUMA node. This proposal
therefore should not promise that sysfs alone can give us generic sub-NUMA or
LLC-to-I/O locality.

## Existing facts to build on

### Kubernetes CPUManager is a node-local placement policy

Kubernetes CPUManager exists because some workloads need cache affinity,
scheduling-latency control, and exclusive CPUs. The static policy now has
multiple placement options, including full physical CPUs, NUMA distribution,
socket alignment, core spreading, strict reservation, and uncore-cache
alignment:

- https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/

We should reuse this as a policy baseline and compatibility reference, but not
assume the kubelet implementation is the right internal boundary for a DRA CPU
driver forever.

### DRA makes ResourceSlice shape an API surface

DRA drivers create and update ResourceSlices. Kubernetes uses them to find
nodes and devices that can satisfy ResourceClaims. ResourceSlices are not just
internal state; they are how the scheduler and users see the driver's resource
model:

- https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/

Recent DRA features make this even more relevant:

- Node Allocatable Resources let DRA devices declare a footprint in standard
  node resources such as `cpu`.
- List-type attributes improve modeling of topology cases where a device has
  adjacency to multiple values, such as multiple PCIe roots.
- Consumable capacity and partitionable devices are the natural Kubernetes
  vocabulary for grouped resources, capacity, and hierarchical device shape.

Therefore allocator internals should not accidentally dictate ResourceSlice
shape. ResourceSlice compatibility should be a deliberate design decision.
However, Node Allocatable Resources and the broader accounting model should be
handled as a separate design thread, because they also affect claim sharing,
QoS/resource accounting, and the KEP-5517 integration path.

### Linux sysfs already exposes the low-level CPU topology

Linux exports CPU topology under
`/sys/devices/system/cpu/cpuX/topology/`, including package, die, cluster,
core, sibling/core/cluster/die cpumasks when supported by the architecture.
The kernel also exposes online/offline/present/possible CPU sets:

- https://docs.kernel.org/admin-guide/cputopology.html

This is already close to the raw data the driver needs. It also matches the
current implementation direction in `pkg/cpuinfo`, which reads sysfs directly
and already tracks socket, NUMA node, cluster, core, SMT sibling, uncore cache,
core type, and online CPUs.

### hwloc is a useful reference and possible backend

hwloc already models hardware locality across packages, NUMA nodes, cores,
PUs, caches, groups, I/O locality, distances, CPU kinds, and asymmetric
topologies:

- https://www.open-mpi.org/projects/hwloc/doc/v2.11.2/a00359.php

Two details are especially relevant:

- hwloc explicitly warns that physical OS indexes can be non-contiguous or
  non-unique, and that core numbers may only be unique within a socket.
- hwloc supports asymmetric topologies, including missing resources due to
  cgroups/cpusets and platforms where different packages expose different
  numbers of cores, SMT threads, or cache levels.

That aligns with PR #210's move from raw `CoreID` to `CoreKey`. It also
suggests that if we need a richer model, using sysfs/hwloc-shaped concepts is
safer than inventing a new abstract hierarchy.

## Goals

- Make the allocator ownership boundary explicit after PR #210.
- Preserve the scoped correctness fix for #35 without expanding PR #210.
- Avoid a new bespoke topology abstraction unless it solves a concrete problem
  that sysfs/hwloc-shaped data cannot solve.
- Provide exact queries for asymmetric topology instead of inferred averages.
- Allow future allocation logic to consume richer locality inputs such as core
  keys, uncore domains, CPU kind, and PCIe-root/NUMA locality where the
  platform exposes them reliably.
- Keep ResourceSlice shape decisions explicit and reviewable, especially for
  0.3.0 and GA readiness.
- Keep the current CPUManager-derived behavior available as the baseline.

## Non-goals

- Do not redesign the ResourceSlice shape in PR #210.
- Do not require hwloc as an immediate runtime dependency.
- Do not replace all CPUManager-derived code in one step.
- Do not solve cluster-level heterogeneous scheduling in this driver. This
  document focuses on node-local discovery, allocation, and projection.
- Do not expose every sysfs field as a public DRA attribute by default.
- Do not treat Node Allocatable Resources, claim sharing, or broader CPU
  accounting as solved by the allocator-boundary work alone.
- Do not assume generic Linux sysfs can provide portable sub-NUMA or
  LLC-to-I/O locality for all platforms.

## Current assumptions we intentionally keep

This proposal is incremental. It does not claim that every current topology
assumption disappears immediately.

- The existing `pkg/cpuinfo.CPUTopology` / `CPUDetails` model remains the
  source of truth for CPU topology facts on Linux.
- In grouped `numanode` mode, the driver currently derives one socket
  attribute for the device from one CPU in that NUMA node. This matches common
  topologies, but it is still an assumption and should stay documented until we
  either enforce it or relax it.
- PCI locality guarantees remain bounded by the kernel interfaces we actually
  have today. Under current generic Linux semantics, that means NUMA/root level
  locality, not a portable sub-NUMA or LLC guarantee.
- `individual` and `grouped` ResourceSlice modes remain the baseline external
  API surface unless a future proposal explicitly changes them.

## Proposed internal boundaries

### 1. Discovery model: evolve the existing topology snapshot

Evolve the existing `pkg/cpuinfo` snapshot so it stays close to Linux sysfs and
explicit about CPU sets.

The snapshot should represent facts, not policy:

- online, present, possible, and offline CPU sets;
- CPU ID to package/socket, die, cluster, core, NUMA node, SMT sibling, CPU
  kind, cache/uncore ID;
- cpumasks for topology domains where available, such as package, die,
  cluster, and core;
- PCIe root to local CPU set, when `--expose-pcie-roots` or a future locality
  feature is enabled;
- derived indexes for exact queries, built from the same snapshot.

This is not a proposal to introduce a second topology universe next to
`CPUTopology`. The important change is conceptual: the discovery layer should
answer "what does the node report?" without encoding allocator assumptions.

### 2. Allocation queries: concrete helpers first

Allocators should not repeatedly scan raw structs or rely on global averages.
They should consume exact queries over the topology snapshot.

```go
topo.CPUDetails.CPUsInNUMANodes(...)
topo.CPUDetails.CPUsInSockets(...)
topo.CPUDetails.CPUsInCoreKeys(...)
topo.CPUDetails.CPUsInUncoreCaches(...)
topo.CPUDetails.CoreKeysInNUMANodes(...)
topo.CPUDetails.CoreKeysInSockets(...)
```

If this starts to sprawl, the first step should be a concrete adapter or
package-local helper around existing `CPUDetails` queries. We should not rush
to introduce an exported cross-package interface until we have at least two
substantially different consumers or implementations that justify it.

The key principle is that allocators ask exact questions and get CPU sets or
stable keys back. They do not divide global counts to infer local shape.

Implementation can remain simple:

- keep `CPUDetails` as the source of CPU facts for now;
- add precomputed indexes only when a hot path or code clarity justifies it;
- keep index construction deterministic and test it with asymmetric fixtures.

This avoids over-engineering while still providing a clear boundary for future
allocators.

### 3. Allocator boundary: package-local before public interface

The current grouped allocation path should become an explicit boundary, but it
does not need to start as a public or cross-package interface.

```go
type AllocationRequest struct {
    AvailableCPUs cpuset.CPUSet
    Count         int
    Constraints   AllocationConstraints
    Topology      *cpuinfo.CPUTopology
}

type AllocationConstraints struct {
    PreferFullCores       bool
    PreferUncoreAlignment bool
    PreferNUMAPacked      bool
    PreferNUMADistributed bool
    RequiredPCIeRoot      string
}
```

This is not a proposal to add all options at once. It is a way to make policy
inputs explicit inside the grouped allocation path, so future work can add one
constraint at a time without rewiring ResourceSlice publication.

Candidate implementations:

- current CPUManager-derived grouped allocator, maintained as the compatibility
  baseline;
- future DRA-specific grouped allocation logic that consumes richer topology
  queries where the platform exposes them reliably;
- `ExplicitCPUSetAllocator`: existing machine-group behavior where an external
  scheduler or opaque config supplies the cpuset.

Only after the project has a real second allocator path should we decide
whether a formal `Allocator` interface adds value or just indirection.

### 4. ResourceSlice projection

ResourceSlice projection should be a separate layer:

```text
topology snapshot -> projection policy -> ResourceSlice devices/attributes/capacity
```

This layer decides what users and the scheduler see:

- individual CPU devices;
- grouped capacity devices by NUMA node, socket, machine, or future locality
  domain;
- attributes such as socket ID, NUMA node ID, core ID, core type, SMT enabled,
  cache ID, PCIe root, and compatibility attributes;
- future topology projections only when the scheduler-visible semantics are
  clear and testable.

The allocator may use richer internal data than the ResourceSlice exposes, but
it must not violate what the ResourceSlice promised. For example, if a
ResourceClaim is matched on a PCIe root, a future allocator should either
allocate CPUs local to that root or the ResourceSlice shape should avoid
implying that guarantee.

Node Allocatable Resource mappings are intentionally excluded from this layer in
this proposal. They belong to a broader resource-accounting and KEP-5517
integration discussion.

## Design alternatives

### Option A: Keep patching the CPUManager-derived allocator

Keep the current code layout and patch it as new topology needs appear.

Pros:

- lowest immediate cost;
- preserves current behavior;
- no new abstraction to review.

Cons:

- drift from kubelet becomes harder to reason about;
- each new locality feature adds another local patch;
- ResourceSlice shape and allocator needs remain implicitly coupled;
- finer-grained locality-aware allocation will likely force larger changes
  anyway.

This is acceptable for PR #210 but probably not enough for 0.3.0 and GA.

### Option B: Fork the allocator intentionally, keep current model

Declare `pkg/cpumanager/cpu_assignment.go` as intentionally forked and continue
evolving it around `pkg/cpuinfo`.

Pros:

- honest about the current state after PR #210;
- minimal package churn;
- lets us fix #35 and some #173 follow-ups incrementally.

Cons:

- still ties allocator shape to today's `pkg/cpuinfo`;
- may duplicate topology/query code as features grow;
- does not force a clean ResourceSlice projection boundary.

This is a reasonable near-term transition, but it should be paired with
decision records and tests that make the forked behavior clear.

### Option C: Add explicit topology, query, allocator, and projection boundaries

Keep the current CPUManager-derived allocator as one implementation, but add
internal boundaries that separate discovery facts, exact queries, allocation
policy, and ResourceSlice projection.

Pros:

- avoids inventing a new topology universe;
- lets sysfs/hwloc-shaped data flow into allocators;
- makes future NUMA/root-aware allocation possible under current kernel
  semantics;
- keeps ResourceSlice compatibility as a deliberate API decision;
- allows incremental migration and comparison against existing behavior.

Cons:

- requires careful scoping to avoid abstraction churn;
- needs tests for both current behavior and asymmetric cases;
- still leaves open how to handle accounting and API changes outside allocator
  internals.

This is my recommended direction.

### Option D: Adopt hwloc directly as the internal model

Use hwloc as the primary topology discovery and representation layer.

Pros:

- avoids reimplementing a mature topology model;
- handles asymmetric topology, object hierarchy, distances, CPU kinds, and I/O
  locality concepts;
- widely understood in HPC/performance-sensitive environments.

Cons:

- likely adds cgo and runtime/library packaging concerns;
- may be more model than this driver needs today;
- Kubernetes/node-agent environments often prefer minimal dependencies;
- still needs a DRA-specific projection and allocator policy layer.

This should remain an evaluation path, not the first step.

## Recommended phased plan

### Phase 0: Merge the scoped correctness fix

Keep PR #210 narrow:

- exact `CPUDetails`/`CoreKey` queries;
- no ResourceSlice shape change;
- no allocator interface change;
- tests for repeated core IDs, asymmetric sockets, and asymmetric uncore/SMT.

### Phase 1: Document allocator ownership

Add a short developer note that explains:

- which files are still intended to track kubelet CPUManager closely;
- which files are intentionally diverged;
- how to evaluate future kubelet resyncs;
- what invariants this driver preserves from CPUManager behavior.

This prevents accidental drift from being mistaken for mechanical vendoring.

### Phase 2: Consolidate exact topology queries without changing behavior

Start by consolidating exact query helpers around the existing
`CPUTopology`/`CPUDetails` model. If a dedicated helper type is needed, keep it
package-local or concrete at first.

Tests should prove:

- asymmetric socket sizes;
- repeated core IDs across sockets;
- asymmetric SMT/offlined CPUs;
- uncore/cache domains with non-uniform CPU counts;
- no behavior change for existing symmetric fixtures.

### Phase 3: Make grouped allocation policy inputs explicit

Move the current grouped allocation path behind an explicit request/constraint
boundary while keeping the current implementation and default behavior.

Only add a formal `Allocator` interface if the code now has at least one real
alternative implementation that makes the interface earn its keep. This phase
should not change ResourceSlice output.

### Phase 4: Prototype a locality-aware allocator

Prototype one concrete improvement that the current allocator cannot express
cleanly. The strongest candidate is #173, but scoped to what the kernel and the
driver can actually guarantee today:

- input: a matched PCIe root or locality domain;
- allocator behavior: intersect available CPUs with CPUs local to that
  root/NUMA domain before applying CPU/core/uncore packing;
- output: CPUSet that satisfies both quantity and the locality guarantee the
  platform can actually expose.

This should be feature-gated or opt-in until ResourceSlice semantics are
reviewed.

### Phase 5: Decide ResourceSlice compatibility policy

Before GA, decide which ResourceSlice shapes are stable:

- Is `individual` mode stable as per-CPU devices?
- Is `grouped` by NUMA node/socket/machine stable?
- Can future grouped devices represent LLC, PCIe root, die, cluster, or other
  domains?
- Which attributes are compatibility attributes, which are informational, and
  which imply allocator guarantees?

This is the real GA-facing decision. It should not be hidden inside allocator
code changes.

### Parallel track: accounting and Node Allocatable Resources

The project should discuss Node Allocatable Resource mappings and broader CPU
accounting separately from allocator boundaries. That work touches:

- claim sharing constraints;
- kubelet/QoS/accounting interactions;
- KEP-5517 integration and migration strategy.

## Testing strategy

Unit tests should use synthetic topology fixtures that intentionally break
symmetric assumptions:

- sockets with different numbers of CPUs;
- cores with different SMT widths;
- core IDs repeated across sockets;
- clusters present on one architecture and absent on another;
- offlined CPUs that make one package partially available;
- uncore/cache domains with different CPU counts;
- PCIe root locality at the granularity the kernel actually reports today.

E2E tests should stay focused on externally visible guarantees:

- ResourceSlices are published with expected devices/attributes/capacity;
- claims in grouped mode get exclusive CPU sets of the requested size;
- allocations do not use reserved CPUs;
- when a future locality-aware mode is enabled, allocated CPUs satisfy the
  selected locality constraint at the documented guarantee level.

## Open questions

- Should the first formal proposal target only allocator boundaries, or also
  ResourceSlice compatibility policy?
- Should the driver keep one default grouped allocator selected by
  configuration, or dynamically vary grouped allocation behavior based on
  request constraints?
- Should sysfs remain the only supported backend for now, with hwloc used only
  as a reference model?
- Which ResourceSlice attributes imply hard allocation guarantees, and which
  are only descriptive?
- Should future locality domains be represented as grouped devices, as
  attributes on existing devices, or both?
- What is the minimum ResourceSlice shape we are willing to call GA-stable?
- When, if ever, should Node Allocatable Resources move from adjacent context
  into a dedicated design proposal for this driver?

## Proposed decision points for maintainers

1. Accept that PR #210 is a correctness fix and should not redesign the
   resource model.
1. Accept that the allocator has crossed the zero-cost kubelet-resync boundary.
1. Decide whether to intentionally own a DRA-specific allocator boundary.
1. Decide whether sysfs-shaped topology data is the preferred internal source
   of truth for now.
1. Decide how to treat ResourceSlice shape compatibility before GA.

## References

- PR #210: https://github.com/kubernetes-sigs/dra-driver-cpu/pull/210
- Issue #35: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/35
- Issue #173: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/173
- Repository sysfs/PCIe locality research:
  https://github.com/kubernetes-sigs/dra-driver-cpu/blob/main/docs/dev/topology-linux-sysfs.md
- Kubernetes CPU Manager policy docs:
  https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/
- Kubernetes Dynamic Resource Allocation docs:
  https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/
- DRA consumable capacity KEP:
  https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md
- DRA partitionable devices KEP:
  https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/4815-dra-partitionable-devices/README.md
- Native Resource Management for DRA:
  https://github.com/kubernetes/enhancements/issues/5517
- Linux CPU topology sysfs docs:
  https://docs.kernel.org/admin-guide/cputopology.html
- hwloc documentation:
  https://www.open-mpi.org/projects/hwloc/doc/v2.11.2/a00359.php
