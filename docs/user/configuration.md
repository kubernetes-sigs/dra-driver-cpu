# Configuration

**IMPORTANT:** The kubelet's CPUManager implements assignment of exclusive CPUs to workloads. The CPUManager and this DRA driver are mutually incompatible and only
one can be enabled at a time on any given node.

## Kubelet configuration prerequisites

You need to disable the CPUManager on the nodes you wish to run this DRA driver.

1. The default settings of the kubelet are compatible with this DRA driver. If you never fine-tuned the kubelet, you are probably fine.
1. Make sure `cpuManagerPolicy: "none"` is set in the kubelet [configuration file](https://kubernetes.io/docs/tasks/administer-cluster/kubelet-config-file/).
1. If you changed the kubelet configuration, restart the kubelet to take effect. **NOTE:** you may need to [delete the CPUManager state file](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#changing-the-cpu-manager-policy).
1. You may now proceed with deploying and configuring this DRA driver.

## Driver configuration

**The configuration file is the preferred configuration mechanism.** Command-line flags are
still supported, but they are kept mainly for backward compatibility and are expected to be
phased out in favour of the configuration file over time (see
[Versioning and backward compatibility](#versioning-and-backward-compatibility) below). If a
field is set both ways, the flag wins, so avoid mixing the two for the same field and configure
new deployments via `driverConfig` / the config file alone.

### Configuration file

When deploying with Helm, set `driverConfig` in your values file. The chart serializes the
map to YAML, stores it in a ConfigMap, mounts it as `/etc/dracpu/config.yaml` inside the
driver container, and passes `--config` automatically.

#### Schema

The config file is a **flat** YAML map - there are no nested groups. All fields are optional
except where noted. Unknown fields are rejected at startup to catch typos early.

| Field                                   | Type   | Default     | Description                                                                                                                                                                                                                                                                                     |
| --------------------------------------- | ------ | ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `apiVersion`                            | string | *(omitted)* | When present, must be `v1alpha1`. Rejected otherwise.                                                                                                                                                                                                                                           |
| `cpuDeviceMode`                         | string | `grouped`   | CPU exposure mode: `individual` or `grouped`.                                                                                                                                                                                                                                                   |
| `groupBy`                               | string | `numanode`  | Grouping strategy when `cpuDeviceMode` is `grouped`: `numanode`, `socket`, or `machine`.                                                                                                                                                                                                        |
| `reservedCPUs`                          | string | *(none)*    | CPUs excluded from allocation, e.g. `"0-1"`.                                                                                                                                                                                                                                                    |
| `hostnameOverride`                      | string | *(none)*    | Override the node hostname the driver registers under.                                                                                                                                                                                                                                          |
| `exposePCIeRoots`                       | bool   | `false`     | Add PCIe root attributes to CPU devices (requires `DRAListTypeAttributes` feature gate).                                                                                                                                                                                                        |
| `kubeconfig`                            | string | *(none)*    | Path to a kubeconfig file (for out-of-cluster use).                                                                                                                                                                                                                                             |
| `publishNodeAllocatableResourceMapping` | bool   | `false`     | Publish `nodeAllocatableResources` mappings in ResourceSlice devices, so the scheduler and kubelet account claimed CPUs as node allocatable `cpu`. Requires the `DRANodeAllocatableResources` feature gate (alpha, 1.37+). See [Workload Configuration Requirements](workload-requirements.md). |

#### Versioning and backward compatibility

The schema is versioned via the optional `apiVersion` field (currently `v1alpha1`). The layout
is intentionally flat for now. If a nested hierarchy is introduced in the future, the
`apiVersion` field will be bumped so that older config files continue to be accepted or produce a
an error.

#### Example

```yaml
# values.yaml
driverConfig:
  apiVersion: v1alpha1
  cpuDeviceMode: grouped
  groupBy: numanode
  reservedCPUs: "0-3"
```

The `apiVersion` field is optional. When present it must equal `v1alpha1` (the current version); any other value is rejected at startup.

`driverConfig` is a single map covering all driver settings — there is no separate Helm value
per field. `args.*` on the other hand exposes individual fields as explicit Helm values and
translates them to CLI flags. Both reach the same driver settings; `args.*` takes priority when
both are set for the same field.

Both `args.*` and `driverConfig` exist during a transition period. The intent is to eventually
deprecate `args.*` in favour of `driverConfig` as the single configuration mechanism. The driver
logs the effective configuration at startup so you can verify which values are active.

### Command-line flags

**NOTE:** Command-line flags are kept mainly for backward compatibility. Prefer the
[configuration file](#configuration-file) above for new deployments.

The driver can be configured with the following command-line flags:

- `--cpu-device-mode`: Sets the mode for exposing CPU devices.
  - `"individual"`: Exposes each allocatable CPU as a separate device in the `ResourceSlice`. This mode provides fine-grained control as it exposes granular information specific to each CPU as device attributes in the `ResourceSlice`.
  - `"grouped" (default)`: Exposes a single device representing a group of CPUs. This mode treats CPUs as a [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md) within the group, improving scalability by reducing the number of API objects.
- `--group-by`: When `--cpu-device-mode` is set to `"grouped"`, this flag determines the grouping strategy.
  - `"numanode"` (default): Groups CPUs by NUMA node.
  - `"socket"`: Groups CPUs by socket.
  - `"machine"`: Groups all allocatable node CPUs into a single machine-wide capacity device. **NOTE**: This mode requires an external scheduler to supply core assignments. See [Custom Opaque CPUSet Allocation Overrides](opaque-cpuset-overrides.md).
- `--reserved-cpus`: Specifies a set of CPUs to be reserved for system and kubelet processes. These CPUs will not be allocatable by the DRA driver and would be excluded from the `ResourceSlice`. The value is a cpuset, e.g., `0-1`. This semantic is the same as the one the kubelet applies with its `static` CPU Manager policy and enabling [`strict-cpu-reservation`](https://kubernetes.io/blog/2024/12/16/cpumanager-strict-cpu-reservation/) flag and specifying the CPUs with the [`reservedSystemCPUs`](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#explicitly-reserved-cpu-list) to be reserved for system daemons. For correct CPU accounting, the number of CPUs reserved with this flag should match the sum of the kubelet's `kubeReserved` and `systemReserved` settings. This ensures the kubelet subtracts the correct number of CPUs from `Node.Status.Allocatable`.
- `--expose-pcie-roots`: If enabled, adds the "resource.kubernetes.io/pcieRoot" standard value to CPU devices, to report the PCIe roots close to each device. Since it always reports values as list, this option requires the cluster Feature Gate `DRAListTypeAttributes` (see KEP 5491) to be enabled. The driver has no way to introspect the cluster Feature Gate, so care must be taken to enable first the Feature Gate then this option.
