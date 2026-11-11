# Driver state persistency

The driver persist the state it can't easily or conveniently recover on a file on the host filesystem.
The state file is meant to allow safe and fast recovery of the driver allocation state across _driver_ restart.
Recovering across _node_ restarts is not supported; to enforce this, the driver stores the kernel-provided
boot identifier (bootID) in its state file. The loader implementation ignores checkpoints from a different boot.
**NOTE**: Checkpoint restoration is not yet wired into driver startup.

## persisted state

The data the driver persists is a stable, versioned, backward/forward compatible counterpart of its internal state.
In other words, the state file is intended to be a stable dump of the driver internal state, not a database or
a generic serialized format. Therefore, we use "checkpoint" as alias for the persisted state, because the persisted
state represents a "checkpoint" in time of the driver state.

What is stored in the checkpoint is what the driver needs to reconstruct its state; data which is not available from
the runtime or that is not stored in the DRA objects. When safe, the driver may prefer to store and retrieve state
in the state file instead of querying the API server.

The stored data objects do not are, in principle, a 1:1 mirror of the internal driver data structures, or even directly
the internal data structures. The reason for the decoupling is to enable support for at least 1 version skew.
How many versions forward and backward compatible should the state file be is left to policies specified elsewhere.

## state machine and state transitions

A key assumption is that the driver does record on durable storage, hence in the state file, all the mutations of
its internal data structures. Every lifecycle change of its internal objects is assumed to be reflected on a checkpoint
update. The recovery (state reload) mechanism holds this key assumptions and can only notice _some_ conflicts that
arise from violation of this assumption. Neither the persistence nor the recovery validate _all_ the state transition
were successfully checkpointed.

**NOTE**: Checkpoint restoration is not yet wired into driver startup.

The state reload is attempted on driver startup, once. Therefore the driver internal state is assumed empty,
and the reload process, if successful, overwrites it completely. The atomic overwrite is also important to prevent
partial loading, which is not supported: the checkpoint is always either loaded completely or not at all.

The main reason for the reload process completely overwrite the internal state is simplicity; differently than
NRI synchronize, which may happen repeatedly while the driver process is running, there are currently no known events
(except bugs, see above) that can cause the internal state to "lose sync" with the durable state; therefore
a single reload on startup seem sufficient and adequately robust.

For the sake of completeness, it's worth mentioning that failing to checkpoint an internal state update is
currently only detectable with code inspection and code review.

## storage format

The checkpoint file follows the principle of simplicity first, everything else equal.
The serialization format of the checkpoint is CBOR. Unlike kubelet, which uses JSON, the CBOR serialization
format is used to discourage users to directly peek in the driver private data. The kubelet checkpoint format
and content has become over the time pretty much an unofficial API; we would like to avoid to go that
path, and CBOR offers a well known, lightweight binary serialization format. Furthermore, `CBOR` was already
an indirect dependency.

## topology fingerprint

the cpu allocation state persisted in the checkpoint makes sense only in relationship against a given
the machine CPU topology. Therefore, the checkpoint stores a fingerprint of the CPU topology against
which the allocation state was computed.
The fingerprint is not meant to be cryptographically secure nor tamper-resistant, as it is meant
to detect accidental corruption caused by preconditions being violated.
The fingerprint is a fixed preamble (magic number + version + reserved space) and then the version-dependent
signature of the topology data.

The topology we use to compute the fingerprint should be the minimal viable subset of CPU
topology information we can use to distinguish topologies. Derived fields, or non-topology-altering
fields should be ignored.

The topology fingerprint match is checked once at startup on loading. When storing checkpoints,
since the driver does not support hotplug/hotunplug, the fingerprint can be pre-computed once at
startup and reused. Therefore the fingerprinting hash needs to be reasonably efficient but has not
strict performance requirements.

## implementation notes

This section describe the first implementation of the state persistence added in version 0.4.0.
For the first implementation, the familiarity and simplicity side carry significant weight, also
considering no driver-specific requirement emerged yet. Therefore, we borrow code from kubelet,
specifically the packages `pkg/kubelet/checkpointmanager` and `pkg/kubelet/checkpointmanager/checksum`
and we adapt them to our needs (minimal changes).
Likewise, the checkpoint approach follows the general direction the kubelet resource manager
(notably cpumanager) use in their latest (1.37) implementation. A notable deviation is however
our usage of internal/external data types to decouple the checkpoint types from the internal
types, to improve the backward/forward compatibility and the data stability.
