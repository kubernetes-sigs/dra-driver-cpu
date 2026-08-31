# Workload Configuration Requirements

Currently, Kubernetes has two separate systems for requesting CPU resources: standard requests in pod/container fields (`pod.spec.resources` or `pod.spec.containers[].resources`) and DRA `ResourceClaim`s.

- The Kube-scheduler uses different plugins to account for these requests, and these plugins are mutually independent. This can lead to node CPU overcommitment because the scheduler might not have a complete picture of all allocated CPUs.

- Kubelet only considers the standard CPU requests in the PodSpec for critical node-level enforcements like [QoS class](https://kubernetes.io/docs/concepts/workloads/pods/pod-qos/) assignment and cgroup hierarchy setup, ignoring CPUs allocated via DRA claims.

[KEP-5517: DRA Node Allocatable Resources](https://github.com/kubernetes/enhancements/issues/5517) addresses the **scheduler accounting** and **cgroup enforcement** parts of this discrepancy: the scheduler counts DRA-allocated CPUs against node allocatable, and the kubelet includes them in pod- and container-level cgroup limits. Changing the **QoS class** logic is an explicit non-goal of the KEP — QoS remains based strictly on standard spec requests and limits, so pods whose only resources come from claims still classify as BestEffort (see [Configuring the right QoS class with claims](#configuring-the-right-qos-class-with-claims) below).

How to configure your workloads depends on whether KEP-5517 accounting is active:

- [Before KEP-5517](#before-kep-5517-before-137-or-alpha-fg-dranodeallocatableresources-disabled) — Kubernetes before 1.37, or 1.37+ with the `DRANodeAllocatableResources` feature gate disabled (its alpha default), or the driver running without `publishNodeAllocatableResourceMapping`.
- [With KEP-5517](#with-kep-5517-137-with-alpha-fg-dranodeallocatableresources-enabled) — Kubernetes 1.37+ with the `DRANodeAllocatableResources` feature gate enabled *and* the driver deployed with `driverConfig.publishNodeAllocatableResourceMapping: true`.

To check which mode is active, inspect the driver's ResourceSlices (`kubectl get resourceslice -o yaml`): the devices include a `nodeAllocatableResources` entry only when the mapping is enabled.

**1-to-1 Claim to Container:** in both modes, this driver enforces that a specific CPU `ResourceClaim` can only be used by *one* container within or across pods. See [Sharing resource claims](feature-support.md#sharing-resource-claims).

## Before KEP-5517 (before 1.37 or alpha FG `DRANodeAllocatableResources` disabled)

The scheduler and kubelet are unaware of claim CPUs, so you MUST configure your pods using one of the following methods to ensure correct behavior and resource accounting:

- **Option A (Preferred): Pod Level Resources (`pod.spec.resources`)**

  - This approach is generally preferred as it more clearly defines the pod's total CPU budget and works well for pods with a mix of containers, some needing exclusive CPUs (requested via DRA) and others using shared CPUs.
  - Set `pod.spec.resources.requests.cpu` and `pod.spec.resources.limits.cpu` to the *sum* of all CPUs requested across all DRA claims used by containers in this pod, PLUS any additional CPUs for containers NOT using DRA claims.
  - Containers using DRA claims may omit `cpu` from their `resources.requests` and `resources.limits`. The Pod Level Resources will govern the QoS class and set cgroup limits at the pod level.

  A complete, runnable version of this pattern is in
  [`hack/examples/pod_with_pod_level_resources.yaml`](../../hack/examples/pod_with_pod_level_resources.yaml).

  ```yaml
  # Example: Pod Level Resources
  spec:
    resources: # Pod Level Resources
      requests:
        cpu: "16" # 10 (exclusive cpu's for claim1) + 4 (exclusive cpu's for claim2) + 2 (shared cpus for sidecar1 and sidecar2)
      limits:
        cpu: "16"
    containers:
      - name: main-app
        image: ...
        resources:
          # Omit CPU requests/limits, or set both to 10
          claims:
            - name: claim1
      - name: worker
        image: ...
        resources:
         # Omit CPU requests/limits, or set both to 4
          claims:
            - name: claim2
      - name: sidecar1
        image: ...
        # Omit CPU resources, or ensure the combined requests/limits for sidecar1 and sidecar2 do not exceed 2.
      - name: sidecar2
        image: ...
        # Omit CPU resources, or ensure the combined requests/limits for sidecar1 and sidecar2 do not exceed 2.
    resourceClaims:
      - name: claim1
        resourceClaimName: cpu-claim-10 # Requests 10 CPUs
      - name: claim2
        resourceClaimName: cpu-claim-4  # Requests 4 CPUs
  ```

- **Option B: Container-Level Resources (No Pod Level Resources)**

  - For each container that uses a DRA CPU claim, set `spec.containers[].resources.requests.cpu` and `spec.containers[].resources.limits.cpu` to be *exactly equal* to the number of CPUs requested in the `ResourceClaim` referenced by that container.

  A complete, runnable version of this pattern is in
  [`hack/examples/pod_with_resource_claim_grouped_mode.yaml`](../../hack/examples/pod_with_resource_claim_grouped_mode.yaml).

  ```yaml
  # Example: Container Level Mirroring
  spec:
    containers:
      - name: my-container
        image: ...
        resources:
          requests:
            cpu: "10" # Must match the CPU count in "claim1"
          limits:
            cpu: "10" # Must match the CPU count in "claim1"
          claims:
            - name: claim1
    resourceClaims:
      - name: claim1
        resourceClaimName: cpu-claim-10 # Requests 10 CPUs
  ```

## With KEP-5517 (1.37+ with alpha FG `DRANodeAllocatableResources` enabled)

Requires the feature gate on the API server, scheduler, and kubelets, and the driver deployed
with `driverConfig.publishNodeAllocatableResourceMapping: true`.

The driver then publishes a `nodeAllocatableResources` mapping on every device, and:

- the **scheduler** counts the claim's CPUs against node allocatable `cpu`. Do not also add
  them to container requests; that counts them twice.
- the **kubelet** adds the claim's CPUs to the cgroup limits when spec limits are declared:
  - **pod-level cgroup**: added to requests (`cpu.weight`) and limits (`cpu.max`);
  - **container-level cgroup**: added to limits (`cpu.max`) only, and only for containers
    that declare a cpu limit. Container `cpu.weight` stays derived from the spec requests
    only; this does not disadvantage the container on its claim CPUs, because the driver
    removes those CPUs from the [shared pool](how-it-works.md) and no other workload
    competes there.

### Recommended Pod Configuration

When deploying workloads with exclusive CPU claims, the recommended pattern is:

1. **Omit `cpu` requests and limits** on the container referencing the exclusive claim. This prevents the kubelet from setting a CFS bandwidth quota (`cpu.max`), avoiding throttling on dedicated cores.
1. **Specify `memory` requests and/or limits** matching the container's memory needs. This assigns the pod to the **Burstable** QoS class, providing better protection against eviction and out-of-memory (OOM) termination than BestEffort.

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: low-latency-exclusive-workload
spec:
  containers:
  - name: exclusive-app
    image: my-app:latest
    resources:
      # Omit cpu requests and limits for the claim container
      requests:
        memory: "4Gi"   # Memory requests ensure Burstable QoS & eviction protection
      limits:
        memory: "4Gi"
      claims:
      - name: exclusive-cpus
  resourceClaims:
  - name: exclusive-cpus
    resourceClaimTemplateName: exclusive-cpus-template
```

A complete, runnable version of this pattern is available in [`hack/examples/pod_with_resource_claim_node_allocatable.yaml`](../../hack/examples/pod_with_resource_claim_node_allocatable.yaml).

### Configuring the Right QoS Class with Claims

The kubelet determines the [QoS class](https://kubernetes.io/docs/concepts/workloads/pods/pod-qos/) strictly from standard `spec` requests and limits — DRA claims do not affect it.

When running workloads with exclusive CPU claims, selecting the appropriate QoS class is essential for [preventing CFS throttling](#preventing-cfs-throttling-on-exclusive-cpus) while maintaining eviction protection.

| QoS Class                        | How to Configure                                                                         | CFS Quota (Throttling) | Eviction & OOM Safety                                                              |
| :------------------------------- | :--------------------------------------------------------------------------------------- | :--------------------- | :--------------------------------------------------------------------------------- |
| **Burstable**<br>*(Recommended)* | Set `memory` requests/limits;<br>**omit `cpu` requests and limits** on claim containers. | ✅ **No quota set**    | ✅ **Better protected than BestEffort** (OOM score scales with the memory request) |
| **Guaranteed**                   | Set `requests == limits` for both `cpu` and `memory` on every container.                 | ⚠️ **Quota set**       | ✅ **Most protected** (lowest OOM score, fixed)                                    |
| **BestEffort**                   | Omit all `cpu` and `memory` requests and limits in all containers.                       | ✅ **No quota set**    | ❌ **Evicted and OOM-killed first**                                                |

- Non-claim containers (such as logging sidecars) can specify standard CPU requests and limits normally; they run on the driver's [shared pool](how-it-works.md).
- When using **pod-level resources** (`pod.spec.resources`), set pod-level `memory` requests and omit pod-level `limits.cpu`.

#### Preventing CFS Throttling on Exclusive CPUs

A primary motivation for requesting exclusive CPUs is to achieve predictable execution by avoiding CFS quota-induced throttling and latency spikes.

When exclusive CPUs are assigned via DRA claims, declaring `limits.cpu` in `pod.spec` (even when attempting to achieve Guaranteed QoS) causes the kubelet to enforce a CFS bandwidth quota that will throttle polling threads during period boundaries. To avoid throttling, follow the **Burstable** recommendation above by omitting `cpu` requests and limits on the claim container (and at the pod level if using pod-level resources).

> For an in-depth technical explanation of Linux cgroup v2, Kubelet cgroup managers, and CFS quota mechanics, see the [Kubelet Cgroups and QoS Deep Dive](../dev/kubelet-cgroups-and-qos.md).

**Reserved environment variables:** the `DRA_CPUSET_*` environment variable prefix is reserved for the driver's CDI injection — do not set variables with this prefix; containers with malformed `DRA_CPUSET_*` values are rejected during creation. See [How it Works](how-it-works.md).

## Extended Resource Claim Status integrations

Kubernetes `status.extendedResourceClaimStatus` is for DRA-backed extended resources. [Extended resource names](https://kubernetes.io/docs/tasks/configure-pod-container/extended-resource/) exclude standard resources such as `cpu` and `memory`, so `extendedResourceName` in a `DeviceClass` or a pod's `status.extendedResourceClaimStatus` is not expected to work with this CPU DRA driver when the container only requests native `cpu`.

For example, a Pod that references a CPU `ResourceClaim` explicitly through `containers[].resources.claims` follows this driver's supported path. A Pod that only patches `status.extendedResourceClaimStatus` with `requestMappings[].resourceName: cpu` does not, because `cpu` is a native resource rather than a DRA-backed extended resource.

For integrations that model native `cpu`, use the Kubernetes node-allocatable DRA status path when available instead.

## Device Health Reporting

The driver reports the health of every device it manages to the kubelet over the DRA `WatchHealthStatus` gRPC API, so the kubelet can reflect it in `pod.status.containerStatuses[].allocatedResourcesStatus`.

- `Healthy`: the default state. The driver has not observed any failure for this device.
- `Unhealthy`: reserved for failures attributable to the device itself. The driver does not currently report this for any condition. A claim that fails to prepare (for example, a CDI spec write error) surfaces as a claim error instead, not a device health change.
- `Unknown`: reported by the kubelet itself and not the driver, when it stops receiving health updates for a device within its lease window (for example, if the driver process is down).
