# Workload Configuration Requirements

Currently, Kubernetes has two separate systems for requesting CPU resources: standard requests in pod/container fields (`pod.spec.resources` or `pod.spec.containers[].resources`) and DRA `ResourceClaim`s.

- The Kube-scheduler uses different plugins to account for these requests, and these plugins are mutually independent. This can lead to node CPU overcommitment because the scheduler might not have a complete picture of all allocated CPUs.

- Kubelet only considers the standard CPU requests in the PodSpec for critical node-level enforcements like [QoS class](https://kubernetes.io/docs/tasks/configure-pod-container/quality-service-pod/) assignment and cgroup hierarchy setup, ignoring CPUs allocated via DRA claims.

This discrepancy is a known issue being addressed by [KEP-5517: Native Resource Management for DRA](https://github.com/kubernetes/enhancements/issues/5517). Until KEP-5517 is implemented, you MUST configure your pods using one of the following methods to ensure correct behavior and resource accounting:

- **Option A (Preferred): Pod Level Resources (`pod.spec.resources`)**

  - This approach is generally preferred as it more clearly defines the pod's total CPU budget and works well for pods with a mix of containers, some needing exclusive CPUs (requested via DRA) and others using shared CPUs.
  - Set `pod.spec.resources.requests.cpu` and `pod.spec.resources.limits.cpu` to the *sum* of all CPUs requested across all DRA claims used by containers in this pod, PLUS any additional CPUs for containers NOT using DRA claims.
  - Containers using DRA claims may omit `cpu` from their `resources.requests` and `resources.limits`. The Pod Level Resources will govern the QoS class and set cgroup limits at the pod level.

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

**1-to-1 Claim to Container:** This driver enforces that a specific CPU `ResourceClaim` can only be used by *one* container within or across pods. See [Sharing resource claims](feature-support.md#sharing-resource-claims).

## Extended Resource Claim Status integrations

Kubernetes `status.extendedResourceClaimStatus` is for DRA-backed extended resources. [Extended resource names](https://kubernetes.io/docs/tasks/configure-pod-container/extended-resource/) exclude standard resources such as `cpu` and `memory`, so `extendedResourceName` in a `DeviceClass` or a pod's `status.extendedResourceClaimStatus` is not expected to work with this CPU DRA driver when the container only requests native `cpu`.

For example, a Pod that references a CPU `ResourceClaim` explicitly through `containers[].resources.claims` follows this driver's supported path. A Pod that only patches `status.extendedResourceClaimStatus` with `requestMappings[].resourceName: cpu` does not, because `cpu` is a native resource rather than a DRA-backed extended resource.

For integrations that model native `cpu`, use the Kubernetes node-allocatable DRA status path when available instead.

## Device Health Reporting

The driver reports the health of every device it manages to the kubelet over the DRA `WatchHealthStatus` gRPC API, so the kubelet can reflect it in `pod.status.containerStatuses[].allocatedResourcesStatus`.

- **`Healthy`**: the default state. The driver has not observed any failure for this device.
- **`Unhealthy`**: the driver observed a failure attributable to this device, for example:
  - writing the CDI spec for a claim failed (marks every device backing that claim)
  - NRI side validation of a previously prepared claim's CPU allocation failed
- **`Unknown`**: reported by the kubelet itself and not the driver, when it stops receiving health updates for a device within its lease window (for example, if the driver process is down).

To keep the kubelet's lease from expiring while everything is fine, the driver resends the full health snapshot of every device every 10 seconds, independent of whether anything changed. Each report carries a health-check timeout of 20 seconds (twice the resend interval), to give the kubelet margin to miss one resend before treating a device as stale. If the driver stops running (or stops making progress) entirely, expect affected devices to flip to `Unknown` within roughly that same window and back to their last-reported state automatically once the driver resumes reporting.
