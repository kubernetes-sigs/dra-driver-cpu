# Configuration

This page covers configuring the driver itself, the kubelet prerequisites required to run it,
and the command-line flags currently being deprecated in favour of the configuration file.

## Driver configuration

The driver supports two configuration mechanisms:

- The config file - the main, preferred way to configure the driver.
- Command-line flags — kept for a small set of important, mostly behavior altering settings and for backward compatibility. Some flags are being deprecated in favor of their config file equivalent (see [Helm: deprecated args values vs driverConfig](#helm-deprecated-args-values-vs-driverconfig) below).

If the same setting is provided both ways, the explicit command-line flag wins, so avoid mixing
the two for the same field.

### Configuration file

The config file is a YAML file passed to the driver via `--config <path>`.

When deploying with Helm, you don't write this file yourself: set the `driverConfig` value in
your values file and the chart serializes it to YAML, stores it in a ConfigMap, mounts it as
`/etc/dracpu/config.yaml` inside the driver container and passes `--config` automatically. For
example, with a `values.yaml` containing:

```yaml
# values.yaml
# Driver configuration
driverConfig:
  cpuDeviceMode: individual
  reservedCPUs: "0-1"
# Other tuning knobs of Helm chart
image:
  tag: v0.2.0
resources:
  requests:
    cpu: "200m"
    memory: "100Mi"
nodeSelector:
  kubernetes.io/os: linux
```

```console
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -f values.yaml
```

Individual fields can also be tuned with `--set` instead of a values file, e.g. to switch
`cpuDeviceMode` to `individual` and reserve CPUs `0-1`:

```shell
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu \
    --set driverConfig.cpuDeviceMode=individual \
    --set driverConfig.reservedCPUs="0-1"
```

#### driverConfig sub-fields

The config file is a flat YAML map - there are no nested groups. All fields are optional
except where noted. Unknown fields are rejected at startup to catch typos early.

These fields only affect the driver's own behavior (CPU allocation, hostname, sysfs path,
etc.). Anything about the driver's Pod itself - image, resources, node placement, and so
on - is configured through other Helm values, not through this file.

`apiVersion` (string)

- When present, must be `v1alpha1`. Rejected otherwise.

`cpuDeviceMode` (string, default: `grouped`)

- `individual`: exposes each allocatable CPU as a separate device in the `ResourceSlice`.
  This mode provides fine-grained control, as it exposes granular information specific
  to each CPU as device attributes.
- `grouped`: exposes a single device representing a group of CPUs. This mode treats CPUs
  as a [consumable capacity](https://github.com/kubernetes/enhancements/blob/master/keps/sig-scheduling/5075-dra-consumable-capacity/README.md)
  within the group, improving scalability by reducing the number of API objects.

`groupBy` (string, default: `numanode`)

- Grouping strategy used when `cpuDeviceMode` is `grouped`.
- `numanode`: groups CPUs by NUMA node.
- `socket`: groups CPUs by socket.
- `machine`: groups all allocatable node CPUs into a single machine-wide capacity device.
  NOTE: this mode requires an external scheduler to supply core assignments. See
  [Custom Opaque CPUSet Allocation Overrides](opaque-cpuset-overrides.md).

`reservedCPUs` (string)

- CPUs excluded from allocation and from the `ResourceSlice`, given as a cpuset, e.g.
  `"0-1"`. This has the same semantics as the kubelet's `static` CPU Manager policy with
  [`strict-cpu-reservation`](https://kubernetes.io/blog/2024/12/16/cpumanager-strict-cpu-reservation/)
  enabled and [`reservedSystemCPUs`](https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#explicitly-reserved-cpu-list)
  set. For correct CPU accounting, the number of CPUs reserved here should match the sum
  of the kubelet's `kubeReserved` and `systemReserved` settings, so that the kubelet
  subtracts the right number of CPUs from `Node.Status.Allocatable`.

`hostnameOverride` (string)

- Override the node hostname the driver registers under.

`kubeconfig` (string)

- Path to a kubeconfig file (for out-of-cluster use).

#### Example

```yaml
# values.yaml
driverConfig:
  apiVersion: v1alpha1
  cpuDeviceMode: grouped
  groupBy: numanode
  reservedCPUs: "0-3"
```

#### Versioning and backward compatibility

The schema is versioned via the optional `apiVersion` field (currently `v1alpha1`). The layout
is intentionally flat for now. If a nested hierarchy is introduced in the future, the
`apiVersion` field will be bumped so that older config files continue to be accepted or produce
an error.

### Helm: deprecated args values vs driverConfig

`driverConfig` is the Helm value that generates the config file described above (a single map
covering all driver settings — there is no separate Helm value per field). `args.*` on the other
hand exposes individual fields as explicit Helm values.

- `args.cpuDeviceMode`, `args.groupBy`, `args.reservedCPUs`, and `args.hostnameOverride` are
  deprecated: instead of being emitted as CLI flags, they are now folded into the generated
  `driverConfig` ConfigMap.
- Both reach the same driver settings; `args.*` takes priority when both are set for the same
  field.
- The intent is to eventually deprecate `args.*` entirely in favour of `driverConfig` as the
  single configuration mechanism. The driver logs the effective configuration at startup so you
  can verify which values are active.

### Command-line flags

**NOTE:** Command-line flags are kept mainly for backward compatibility. Prefer the
[configuration file](#configuration-file) above for new deployments.

`--cpu-device-mode`, `--group-by`, `--reserved-cpus`, `--hostname-override`, `--sysfs-overlay`
are deprecated in favour of their config file equivalents and will be removed in a future
release ([issue #245](https://github.com/kubernetes-sigs/dra-driver-cpu/issues/245)). Each is
marked as deprecated in `--help`, and passing one explicitly logs a startup warning.

- `--cpu-device-mode` → `cpuDeviceMode`
- `--group-by` → `groupBy`
- `--reserved-cpus` → `reservedCPUs`
- `--hostname-override` → `hostnameOverride`
- `--sysfs-overlay` → `sysfsOverlay`

The remaining flags aren't part of that deprecation:

- `--config`: path to the config file described above.
- `--kubeconfig`: path to a kubeconfig file, for out-of-cluster use. Also settable via the `kubeconfig` config field.
- `--bind-address`: address the metrics server listens on.
- `--expose-pcie-roots`: adds the `resource.kubernetes.io/pcieRoot` standard value to CPU
  devices, reporting the PCIe roots close to each device. Since it always reports values
  as a list, this option requires the cluster feature gate `DRAListTypeAttributes` (see
  KEP 5491) to be enabled. The driver cannot introspect the cluster feature gate, so
  enable the feature gate first and this option second. Unlike the flags above, it is not
  deprecated and has no config file equivalent — it is intentionally excluded from the
  config file (see [driverConfig sub-fields](#driverconfig-sub-fields) above) and stays a
  standalone flag (or Helm `args.exposePCIeRoots`).

## Kubelet configuration prerequisites

**IMPORTANT:** The kubelet's CPUManager implements assignment of exclusive CPUs to workloads. The CPUManager and this DRA driver are mutually incompatible and only
one can be enabled at a time on any given node. You need to disable the CPUManager on the nodes you wish to run this DRA driver.

1. The default settings of the kubelet are compatible with this DRA driver. If you never fine-tuned the kubelet, you are probably fine.
1. Make sure `cpuManagerPolicy: "none"` is set in the kubelet [configuration file](https://kubernetes.io/docs/tasks/administer-cluster/kubelet-config-file/).
1. If you changed the kubelet configuration, restart the kubelet to take effect. **NOTE:** you may need to [delete the CPUManager state file](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/#changing-the-cpu-manager-policy).
1. You may now proceed with deploying and configuring this DRA driver.
