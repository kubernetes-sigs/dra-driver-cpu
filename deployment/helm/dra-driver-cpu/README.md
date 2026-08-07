# dra-driver-cpu Helm Chart

Kubernetes DRA driver for managing CPU resources with topology-aware allocation, exclusive CPU assignment, and shared CPU pool management via the Dynamic Resource Allocation framework.

## Installation
From a stable release:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu --version 0.2.0 -n kube-system
```

From a local checkout:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system
```

To override values at install time:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu --version 0.2.0 -n kube-system \
  --set args.cpuDeviceMode=individual \
  --set args.reservedCPUs="0-1"
```

Parameters can be set at install time using `--set` or a custom values file:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu --version 0.2.0 -n kube-system --set args.logLevel=4
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu --version 0.2.0 -n kube-system -f my-values.yaml
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for scheduling the DaemonSet pods |
| args.cpuDeviceMode | string | `""` | **Deprecated:** folded into the generated `driverConfig` ConfigMap and takes priority over it; use `driverConfig.cpuDeviceMode` instead. CPU exposure mode: `grouped` (expose NUMA nodes or sockets as devices) or `individual` (expose each CPU as a device); defaults to `grouped` when empty. |
| args.exposePCIeRoots | bool | `false` | Discover and expose PCIe roots as device attributes. Requires the `DRAListTypeAttributes=true` feature gate in the cluster. Not configurable via `driverConfig`; use this flag instead |
| args.groupBy | string | `""` | **Deprecated:** folded into the generated `driverConfig` ConfigMap and takes priority over it; use `driverConfig.groupBy` instead. Grouping criteria when `cpuDeviceMode=grouped`: `numanode`, `socket` or `machine`; defaults to `numanode` when empty. |
| args.hostnameOverride | string | `""` | **Deprecated:** folded into the generated `driverConfig` ConfigMap and takes priority over it; use `driverConfig.hostnameOverride` instead. Overrides the node name the driver registers under. |
| args.logLevel | int | `4` | Log verbosity level passed as `--v` |
| args.reservedCPUs | string | `""` | **Deprecated:** folded into the generated `driverConfig` ConfigMap and takes priority over it; use `driverConfig.reservedCPUs` instead. CPUs reserved for the OS and kubelet, excluded from DRA management (e.g. `"0-1"`). |
| driverConfig | object | `{}` | Driver config file contents. When non-empty, or when a deprecated `args.*` field below is set, a ConfigMap is created and mounted into the driver container as /etc/dracpu/config.yaml. `args.*` fields that mirror a deprecated CLI flag (`cpuDeviceMode`, `groupBy`, `reservedCPUs`, `hostnameOverride`) are folded into the generated config automatically and take priority over the same field set here. Example:   driverConfig:     cpuDeviceMode: individual     groupBy: socket     reservedCPUs: "0-3" |
| extraArgs | list | `[]` | Extra command-line arguments appended to the driver arguments |
| extraVolumeMounts | list | `[]` | Extra volume mounts for the driver container |
| extraVolumes | list | `[]` | Extra volumes for the DaemonSet pod |
| fullnameOverride | string | `""` | Override the full release name |
| healthzPath | string | `"/healthz"` | Path for liveness and readiness probes |
| healthzPort | int | `8080` | Port the HTTP server binds to; used for the container port and probes |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| image.repository | string | `"registry.k8s.io/dra-driver-cpu/dra-driver-cpu"` | Container image repository |
| image.tag | string | `""` | Image tag; defaults to `.Chart.AppVersion` when empty, which is set to the release tag at package time |
| imagePullSecrets | list | `[]` | List of image pull secrets |
| kubeletRootDir | string | `"/var/lib/kubelet"` | Kubelet root directory, matching the kubelet's own `--root-dir`. The driver registers under `<root>/plugins_registry` and creates its socket under `<root>/plugins/<driver-name>`, and the hostPath mounts come from this same value, so the two cannot come apart. Only set this if the kubelet does not use the default. |
| nameOverride | string | `""` | Override the chart name |
| nodeSelector | object | `{}` | Node selector for scheduling the DaemonSet pods |
| podAnnotations | object | `{}` | Annotations to add to pods |
| podLabels | object | `{}` | Extra labels to add to pods |
| rbac.create | bool | `true` | Create RBAC resources (ClusterRole and ClusterRoleBinding) |
| resources.limits | object | `{}` | Resource limits (unset by default) |
| resources.requests.cpu | string | `"100m"` | CPU resource request |
| resources.requests.memory | string | `"50Mi"` | Memory resource request |
| serviceAccount.annotations | object | `{}` | Annotations to add to the ServiceAccount |
| tolerations | list | `[{"effect":"NoSchedule","operator":"Exists"}]` | Node tolerations; defaults to tolerating all NoSchedule taints |

## Uninstallation

```bash
helm uninstall dra-driver-cpu -n kube-system
```
