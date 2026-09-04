# SMT layout encoding

`dra.cpu/smtLayout` is a compact representation of logical-CPU sibling pairs.

## design

The encoding is composed by entries separated by `;`.
Each entry has the generic format `cpuset[>S]` where `cpuset` is the encoding
of a linux `cpuset`, `>` is the literal `>` sign and `S` is a positive integer
representing the "stride" aka the offset, the absolute difference between 2
sibling CPU IDs represented as positive integers..

Examples:

- `0-7>4` means CPUs `0` and `4`, `1` and `5`, `2` and `6`, and `3` and `7`
  are sibling pairs.
- `8-11` has no `>stride`, so CPUs `8` through `11` have no sibling.
- `0-7>4;8-11>2;12-15` combines two strided sibling sets and one set of
  sibling-less CPUs.

An entry with `>N` contains both members of every pair. `N` is a positive CPU-ID
offset: starting with the lowest unpaired CPU in the entry, its sibling is the
CPU `N` positions above it. An entry without `>N` lists sibling-less CPUs (e.g
no SMT for some or all CPUs).

The encoded representation is stable. The entries with a stride are emitted
in ascending numeric stride order.
The sibling-less entry, if any, is emitted last.
The encoding describes the node-wide topology, including CPUs that may be reserved.
Consumers combining it with `dra.cpu/cpuIDs` must use `cpuIDs` to identify the
allocatable CPUs in a particular grouped device.

## consuming the encoding

The driver reports the full-machine thread sibling mapping on each device.
Consumers would need to intersect the reconstructed mapping to the subset
of each cpus running on each grouped devices.
The `dra.cpu/cpuIDs` attribute reports the cpuset each grouped device represents,
so it can be used to reconstruct the per-device mapping.

Future versions of the drivers can start reporting the pre-populated per-device
`smtLayout`. Note in this case intersecting with per-grouped device cpuset would
be still correct, albeit redundant.

## encoding examples

The following are concise, `lscpu`-style topology views reconstructed from the
real-world fixtures in [`pkg/device/smt_test.go`](../../pkg/device/smt_test.go).
`smtLayout` is shown after the topology it encodes. CPU IDs, their NUMA placement,
and therefore the stride are assigned by firmware and the operating system; the
same processor can have a different encoding on another machine.

### Intel Core i7-12850HX

```text
CPU(s): 24; On-line CPU(s) list: 0-23; Socket(s): 1; Core(s) per socket: 16; NUMA node(s): 1
Thread(s) per core: 2 for CPUs 8-23, 1 for CPUs 0-7
NUMA node0 CPU(s): 0-23
smtLayout: 8-23>8;0-7
```

This hybrid topology has eight SMT pairs (`8` with `16` through `15` with `23`)
and eight single-threaded CPUs (`0-7`).

### Two Intel Xeon Gold 6320 processors

```text
CPU(s): 80; On-line CPU(s) list: 0-79; Socket(s): 2; Core(s) per socket: 20; Thread(s) per core: 2; NUMA node(s): 4
NUMA node0 CPU(s): 0-9,40-49; node1: 10-19,50-59; node2: 20-29,60-69; node3: 30-39,70-79
smtLayout: 0-79>40
```

The alternate fixture has the same sibling pairs and encoding, but its NUMA
placement is interleaved:

```text
NUMA node0 CPU(s): 0,4,...,76; node1: 1,5,...,77; node2: 2,6,...,78; node3: 3,7,...,79
smtLayout: 0-79>40
```

### Two AMD EPYC 7742 processors (NPS=4)

```text
CPU(s): 256; On-line CPU(s) list: 0-255; Socket(s): 2; Core(s) per socket: 64; Thread(s) per core: 2; NUMA node(s): 8
NUMA node0 CPU(s): 0-15,128-143; node1: 16-31,144-159; node2: 32-47,160-175; node3: 48-63,176-191
NUMA node4 CPU(s): 64-79,192-207; node5: 80-95,208-223; node6: 96-111,224-239; node7: 112-127,240-255
smtLayout: 0-255>128
```

### Two AMD EPYC 9654 processors

```text
CPU(s): 384; On-line CPU(s) list: 0-383; Socket(s): 2; Core(s) per socket: 96; Thread(s) per core: 2; NUMA node(s): 2
NUMA node0 CPU(s): 0-95,192-287; node1: 96-191,288-383
smtLayout: 0-383>192
```

### AMD EPYC processors with SMT disabled

```text
AMD EPYC 7702P: CPU(s): 64; On-line CPU(s) list: 0-63; Socket(s): 1; Core(s) per socket: 64; Thread(s) per core: 1; NUMA node(s): 1
NUMA node0 CPU(s): 0-63
smtLayout: 0-63

Two AMD EPYC 7303: CPU(s): 64; On-line CPU(s) list: 0-63; Socket(s): 2; Core(s) per socket: 32; Thread(s) per core: 1; NUMA node(s): 2
NUMA node0 CPU(s): 0-31; node1: 32-63
smtLayout: 0-63

Two AMD EPYC 9654 with sub-NUMA clustering: CPU(s): 192; On-line CPU(s) list: 0-191; Socket(s): 2; Core(s) per socket: 96; Thread(s) per core: 1; NUMA node(s): 4
NUMA node0 CPU(s): 0-47; node1: 48-95; node2: 96-143; node3: 144-191
smtLayout: 0-191
```

### AMD EPYC 9754

```text
CPU(s): 256; On-line CPU(s) list: 0-255; Socket(s): 1; Core(s) per socket: 128; Thread(s) per core: 2; NUMA node(s): 1
NUMA node0 CPU(s): 0-255
smtLayout: 0-255>128
```

### Two Intel Xeon Platinum 8490H processors

```text
CPU(s): 240; On-line CPU(s) list: 0-239; Socket(s): 2; Core(s) per socket: 60; Thread(s) per core: 2; NUMA node(s): 4
NUMA node0 CPU(s): 0-29,120-149; node1: 30-59,150-179; node2: 60-89,180-209; node3: 90-119,210-239
smtLayout: 0-239>120
```
