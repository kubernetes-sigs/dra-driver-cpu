# CPU microtopology

## core identity and uniqueness

The cpumanager code expects logical cores to have unique identifiers per node.
At least on `x86_64`, real HW topology usually expose per-socket unique identifier.

To overcome this limitation, we have helpers which create a `CPUTopology` instance,
based on the raw one detected from the system, in which each core is identified
by its lowest logical CPU ID, following kubelet's convention.

Cores are grouped by socket, cluster, and hardware core ID before remapping.
The copy includes unavailable CPUs so reservation does not change core identity.
Discovery and published attributes retain their hardware IDs.
