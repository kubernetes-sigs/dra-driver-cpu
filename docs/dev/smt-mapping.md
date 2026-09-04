# SMT sibling-map encoding

`dra.cpu/smtMap` is a compact representation of logical-CPU sibling pairs.

The encoding is composed by entries separated by `;`.
Each entry has the generic format `cpuset[>S]` where `cpuset` is the encoding
of a linux `cpuset`, `>` is the literal `>` sign and `S` is a positive integer
representing the "stride" aka the offset akacthe absolute difference between 2
sibling CPU IDs.

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
