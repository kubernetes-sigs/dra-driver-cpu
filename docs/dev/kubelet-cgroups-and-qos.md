# Deep Dive: Cgroup v2, QoS Classes, and CPU Enforcement in Kubernetes

This document provides a technical deep-dive into how Kubernetes organizes Linux control groups under **cgroup v2**, calculates and applies Quality of Service (QoS) classes, manages cgroup hierarchies at node, QoS, pod, and container levels, and enforces CPU allocations. It also documents the architectural relationship between Kubelet's cgroup management and out-of-tree Dynamic Resource Allocation (DRA) drivers such as `dra-driver-cpu`.

## 1. The Linux Cgroup v2 Hierarchy in Kubernetes

Under cgroup v2 with the `systemd` cgroup driver, Kubernetes organizes container processes into a strictly hierarchical tree under `/sys/fs/cgroup/kubepods.slice`:

```
/sys/fs/cgroup/
  └── kubepods.slice (Root Node Allocatable Cgroup)
        ├── kubepods-burstable.slice (Burstable QoS Slice)
        │     ├── kubepods-burstable-pod<BURSTABLE_POD_UID_1>.slice (Pod Cgroup)
        │     │     └── <container-id-1> (Container Cgroup)
        │     └── kubepods-burstable-pod<BURSTABLE_POD_UID_2>.slice
        ├── kubepods-besteffort.slice (BestEffort QoS Slice)
        │     └── kubepods-besteffort-pod<BESTEFFORT_POD_UID_1>.slice (Pod Cgroup)
        │           └── <container-id-1> (Container Cgroup)
        ├── kubepods-pod<GUARANTEED_POD_UID_1>.slice (Guaranteed Pod Cgroup)
        │     └── <container-id-1> (Container Cgroup)
        └── kubepods-pod<GUARANTEED_POD_UID_2>.slice (Guaranteed Pod Cgroup)
              └── <container-id-1> (Container Cgroup)
```

- Top-level QoS cgroups are only created for `Burstable` and `BestEffort`. Guaranteed pods have 1:1 resource reservations directly against the root Node Allocatable cgroup (`kubepods.slice`) and do not share an overcommitted QoS pool. Only `Burstable` and `BestEffort` require intermediate QoS slices so Kubelet can dynamically adjust aggregate CPU shares (`cpu.weight`) and pin BestEffort to the floor of `MinShares = 2` (`cpu.weight = 1`).

In Linux cgroups, resource constraints enforced on an ancestor cgroup are **strictly binding** on all nested descendants:

- CPU execution by a thread in a leaf container charges against that container's cgroup, **and** walks up the tree charging against the parent `pod.slice`, the QoS tier slice (if applicable), and `kubepods.slice`.
- If a parent cgroup reaches its CFS quota ceiling, the Linux kernel CFS scheduler throttles **all threads in all containers inside that parent cgroup**, regardless of whether the individual container cgroup has unlimited quota (`cpu.max = max`).

## 2. QoS Class Evaluation and Cgroup Settings

Kubernetes defines 3 QoS classes strictly evaluated from standard container specifications in `pod.spec`. DRA claims are ignored during QoS classification.

### 2.1 QoS Classification Rules

Kubernetes evaluates QoS classes strictly from standard resource requirements in `pod.spec` (DRA claims are ignored during QoS classification).

#### Standard Container-Level Resources (without Pod Level Resources)

When Pod Level Resources (`pod.spec.resources`) is not set, Kubelet evaluates requests and limits across all containers (including init containers):

```
                      +-----------------------------+
                      |    Are any requests or      |
                      |   limits set in pod spec?   |
                      +-----------------------------+
                                     |
                         +-----------+-----------+
                         | No                    | Yes
                         v                       v
               +------------------+    +--------------------------------+
               |    BestEffort    |    |    Does EVERY container set    |
               +------------------+    |      non-zero req == lim       |
                                       |    for BOTH cpu and memory?    |
                                       +--------------------------------+
                                                     |
                                         +-----------+-----------+
                                         | Yes                   | No
                                         v                       v
                               +------------------+    +------------------+
                               |    Guaranteed    |    |    Burstable     |
                               +------------------+    +------------------+
```

#### With Pod Level Resources

When the `PodLevelResources` feature gate is enabled, Kubelet checks whether resources are declared in `pod.spec.resources` before falling back to container-level evaluation:

```
                    +------------------------------------+
                    |   Are Pod Level Resources set in   |
                    |        pod.spec.resources?         |
                    +------------------------------------+
                                       |
                          +------------+------------+
                          | No                      | Yes
                          v                         v
           +-----------------------------+   +-----------------------------+
           | Evaluate Standard Container-|   |   Does pod.spec.resources   |
           | Level Resources             |   |   set non-zero req == lim   |
           | (see diagram above)         |   |  for BOTH cpu and memory?   |
           +-----------------------------+   +-----------------------------+
                                                            |
                                               +------------+------------+
                                               | Yes                     | No
                                               v                         v
                                        +------------+            +------------+
                                        | Guaranteed |            | Burstable  |
                                        +------------+            +------------+
```

### 2.2 Cgroup Config Matrix

| Cgroup Controller                      | Scope                 | Guaranteed                                                                                                                   | Burstable                                                                                                                                                                                            | BestEffort                       |
| :------------------------------------- | :-------------------- | :--------------------------------------------------------------------------------------------------------------------------- | :--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | :------------------------------- |
| **Hard Ceiling (`cpu.max`)**           | **Container**         | • `unlimited` (static CPU policy and integer requests)<br>• `limits.cpu` (fractional requests or static CPU policy disabled) | • `unlimited` (if `limits.cpu` omitted)<br>• `limits.cpu` (if `limits.cpu` declared)                                                                                                                 | `unlimited`                      |
|                                        | **Pod (`pod.slice`)** | • `unlimited` (if any container is assigned exclusive CPUs by kubelet)<br>• sum of limits (otherwise)                        | • `unlimited` (if any container omits `limits.cpu` and no pod-level CPU limit is set)<br>• sum of limits / pod limit (if all containers set `limits.cpu`, or `pod.spec.resources.limits.cpu` is set) | `unlimited`                      |
| **Proportional Shares (`cpu.weight`)** | **Container**         | Proportional to `requests.cpu`                                                                                               | Proportional to `requests.cpu`                                                                                                                                                                       | `1` (`MinShares = 2`)            |
|                                        | **Pod (`pod.slice`)** | Sum of container requests                                                                                                    | Sum of container requests                                                                                                                                                                            | `1` (`MinShares = 2`)            |
|                                        | **Parent Slice**      | *(None — direct child of root)*                                                                                              | Proportional to active requests                                                                                                                                                                      | Clamped to `1` (`MinShares = 2`) |
| **Core Pinning (`cpuset.cpus`)**       | **Container**         | • Dedicated physical cores (static policy & integer requests)<br>• Shared CPU pool (fractional or policy disabled)           | Shared CPU pool                                                                                                                                                                                      | Shared CPU pool                  |
| **OOM Priority (`oom_score_adj`)**     | **Process**           | `-997` (OOM protected)                                                                                                       | `3`–`999` (scaled by memory request)                                                                                                                                                                 | `1000` (killed first)            |

**Note:** Setting a non-zero pod-level CPU limit (`pod.spec.resources.limits.cpu`) causes Kubelet to configure a CFS bandwidth quota on `pod.slice` matching that limit, even when individual containers omit CPU limits. To maintain zero CFS throttling on exclusive cores when using Pod-Level Resources (PLR), `limits.cpu` must be omitted at **both** the container and pod levels.

## 3. CFS Quota Enforcement and the "Exclusive CPU" Problem

### 3.1 Why CFS Quota Throttles Exclusive CPUs

In Linux cgroup v2, Completely Fair Scheduler (CFS) bandwidth control (`cpu.max`) caps cumulative CPU runtime over a 100ms window.

For workloads running at 100% duty cycle on exclusive physical cores (e.g. DPDK polling threads), minor kernel accounting jitter (timers, context switches, softirqs) routinely causes measured execution time to slightly exceed the quota before the window resets. As documented in [Kubernetes Issue #70585](https://github.com/kubernetes/kubernetes/issues/70585), this causes the kernel to **throttle the cgroup** for several milliseconds, introducing latency spikes and defeating hardware core isolation.

### 3.2 How In-Tree Static CPU Policy Solves This

To eliminate this throttling, Kubelet automatically disables CFS quota whenever exclusive CPUs are assigned via the static CPU policy.

- Kubelet checks if the container was allocated exclusive integer cores. If so, it passes `CpuQuota = -1` to the container runtime → Container cgroup gets `cpu.max = max`.
- At the pod level, Kubelet checks if *any* container in the pod has exclusive cores. If so, it disables CFS quota on the parent `pod.slice` → Parent `pod.slice` gets `cpu.max = max`.
- Even if other containers in the pod have fractional requests (e.g. Container B = 500m), Container B is capped by its *own* leaf cgroup (`cpu.max = 50000 100000`), while the parent `pod.slice` remains unthrottled.

## 4. DRA Driver and NRI Architectural Boundaries

When exclusive CPUs are allocated through **DRA** (`dra-driver-cpu`):

### 4.1 Kubelet Blind Spot

Kubelet's quota disabling logic only inspects the internal state of the **in-tree `CPUManager`**. Because `CPUManager` has no visibility into out-of-tree DRA claims:

- Kubelet assumes no exclusive CPUs are present.
- If a pod specifies `limits.cpu` (e.g. attempting to achieve Guaranteed QoS), Kubelet calculates a positive quota and enforces it on **both** the container cgroup and the parent `pod.slice`.

### 4.2 Why NRI Plugins Cannot Unset Pod Quotas

- **NRI API Scope:** The NRI specification (`github.com/containerd/nri`) only allows mutations on containers via `ContainerAdjustment`. Pod-level hooks (`RunPodSandbox`, `UpdatePodSandbox`) are strictly read-only notifications; there is no `PodAdjustment` type in the NRI specification.
- **CRI Lifecycle Ordering:** Kubelet creates `pod.slice` on the host systemd hierarchy *before* CRI creates the pod sandbox. Container runtimes (containerd/CRI-O) do not manage `pod.slice` limits.
- **Why Direct Host Cgroupfs Writes Are Discouraged:**
  - **Systemd Desync:** Systemd manages unit properties. Direct filesystem writes to `/sys/fs/cgroup/.../cpu.max` can be overwritten during daemon reloads or slice adjustments.
  - **Path Fragility:** Resolving cgroup paths varies across containerd, CRI-O, and Linux distributions.
  - **Blast Radius:** Unsetting quota on `pod.slice` removes the ceiling for *all* containers in the pod, including shared sidecars that were intended to be bounded.

## 5. The Clean Architectural Solution: Burstable with Omitted CPU Limits

As recognized in [KEP-5517](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5517-dra-node-allocatable-resources/README.md#handling-kubelet-disabling-quota-with-exclusive-cpus), the clean, native mechanism to achieve completely unthrottled execution is **spec design**:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: low-latency-pod
spec:
  containers:
  - name: exclusive-app
    image: my-app:latest
    resources:
      requests:
        # 1. CPU requests and limits are completely omitted here
        memory: "4Gi"   # 2. Set memory requests, limits. Promotes pod to Burstable (avoids BestEffort's first-to-evict ranking)
      limits:
        memory: "4Gi"
      claims:
      - name: exclusive-cpus # 3. DRA pins container to exclusive CPUs
  resourceClaims:
  - name: exclusive-cpus
    resourceClaimTemplateName: exclusive-cpus-template
```

### Why this works across the entire cgroup tree:

- **Container cgroup:** Because `limits.cpu` is omitted, Kubelet defaults the container quota to unlimited → Container `cpu.max = max`.
- **Pod cgroup (`pod.slice`):** Because at least one container omits CPU limits (and no pod-level CPU limit is declared in `pod.spec.resources`), Kubelet skips setting a quota on `pod.slice` (`cpuLimitsDeclared = false` in `pkg/kubelet/cm/helpers_linux.go:200-217`) → `pod.slice/cpu.max = max`.
- **QoS tier slices:** `kubepods-burstable.slice` and `kubepods.slice` never enforce CFS quotas.
- **Result:** **Zero CFS throttling anywhere in the cgroup hierarchy.** The workload enjoys complete, uninhibited execution on its dedicated physical cores.

When using **Pod-Level Resources** (`pod.spec.resources`), the same principle applies: declare memory requests/limits at the pod level to secure Burstable QoS, but omit pod-level `limits.cpu`:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: low-latency-pod-plr
spec:
  resources:
    requests:
      memory: "4Gi"
    limits:
      memory: "4Gi"
    # Omit limits.cpu at the pod level to keep pod.slice/cpu.max = max
  containers:
  - name: exclusive-app
    image: my-app:latest
    resources:
      claims:
      - name: exclusive-cpus
  resourceClaims:
  - name: exclusive-cpus
    resourceClaimTemplateName: exclusive-cpus-template
```
