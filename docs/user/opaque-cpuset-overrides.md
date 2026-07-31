# Custom Opaque CPUSet Allocation Overrides

> [!NOTE]
> **Audience: scheduler-plugin authors and platform integrators**, not workload authors. This
> page documents an integration contract for external schedulers; no in-tree scheduler
> implements it today. Regular workloads should use the default `numanode`/`socket` grouping
> instead.

When using `grouped` device mode with the `groupBy: machine` configuration, the DRA driver does not perform automatic topology-aware CPU allocation. Instead, an explicit core assignment must be provided via the `cpuset` field in the claim's opaque configuration parameters.

The Kubelet driver parses this configuration at prepare time from the claim's allocation status (`status.allocation.devices.config`). The control plane (typically scheduling plugin) is responsible for injecting this configuration block into the allocation result when binding the claim.

## Opaque Parameters Schema (`v1alpha1`)

The `opaque.parameters` field must conform to the following schema:

| Field              | Type   | Description                                                                                |
| :----------------- | :----- | :----------------------------------------------------------------------------------------- |
| `apiVersion`       | string | Must be set to `v1alpha1`.                                                                 |
| `cpuConfig`        | object | Container object for CPU configurations.                                                   |
| `cpuConfig.cpuset` | string | Specifies the list of CPU cores in standard Linux cpuset format (e.g. `"2-5"`, `"0,4-6"`). |

## Example of a Fully Allocated ResourceClaim with Opaque Configuration

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: exclusive-cores-claim
  namespace: default
  uid: claim-uid-123456
spec:
  devices:
    requests:
    - name: cpu-request-1
      exactly:
        deviceClassName: dra.cpu
        count: 4
status:
  allocation:
    devices:
      results:
      - request: cpu-request-1
        driver: dra.cpu
        pool: test-node
        device: cpudevmachine
        consumedCapacity:
          dra.cpu/cpu: "4"
      config:  # Added by external scheduler
      - source: FromClaim
        requests:
        - cpu-request-1
        opaque:
          driver: dra.cpu
          parameters:
            apiVersion: v1alpha1
            cpuConfig:
              cpuset: "2-5"
```

The driver allocates the specified cores directly (after validating that they are allocatable and not reserved). If it is omitted, resource preparation will fail and pod startup will be rejected.

> [!IMPORTANT]
> **Validation Rules:**
>
> - The `cpuset` value must be specified in standard Linux cpuset format representing ranges and/or individual cores (e.g. `"2-5"`, `"1-10,15"`).
> - **Per-Request Configurations Only**: In `status.allocation.devices.config[]`, every configuration block's `requests` array must map to exactly one request in the claim. We do not support multiple claim requests mapping to the same cpuset config.
> - **ResourceClaim Source**: Opaque configurations must originate from the `ResourceClaim`. Configurations defined in DeviceClass (`spec.config[]`) are not supported and will result in a validation error. Since a configuration in the `DeviceClass` applies to all claims referencing it, configuring a `cpuset` there would assign the same CPUs to multiple claims, failing allocation due to conflict between claims.
> - **CPUSet Validation**: The driver verifies that the custom cpuset is valid for the host machine and is currently allocatable. It checks that:
>   - The cores are part of the node's online CPUs.
>   - The cores are not reserved using the driver's `--reserved-cpus` configuration flag.
> - **Error Handling**: If validation fails (e.g. core conflict, size mismatch, duplicate target, or offline cores), the driver returns a failure immediately in Kubelet's `PrepareResourceClaims` hook, causing pod startup to fail.
