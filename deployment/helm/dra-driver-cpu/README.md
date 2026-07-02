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
| args.cpuDeviceMode | string | `""` | CPU exposure mode: `grouped` (expose NUMA nodes or sockets as devices) or `individual` (expose each CPU as a device); when empty, defers to `driverConfig.cpuDeviceMode` or the built-in default (`grouped`) |
| args.exposePCIeRoots | bool | `false` | Discover and expose PCIe roots as device attributes. Requires the `DRAListTypeAttributes=true` feature gate in the cluster |
| args.groupBy | string | `""` | Grouping criteria when `cpuDeviceMode=grouped`: `numanode`, `socket` or `machine`; when empty, defers to `driverConfig.groupBy` or the built-in default (`numanode`) |
| args.hostnameOverride | string | `""` | Override the node name the driver registers under; omitted when empty |
| args.logLevel | int | `4` | Log verbosity level passed as `--v` |
| args.reservedCPUs | string | `""` | CPUs reserved for the OS and kubelet, excluded from DRA management (e.g. `"0-1"`); omitted when empty |
| driverConfig | object | `{}` | Driver config file contents. When non-empty, a ConfigMap is created and mounted into the driver container as /etc/dracpu/config.yaml. Fields in `args.*` that are non-empty always take priority over values set here. Example:   driverConfig:     cpuDeviceMode: individual     groupBy: socket     reservedCPUs: "0-3" |
| fullnameOverride | string | `""` | Override the full release name |
| healthzPath | string | `"/healthz"` | Path for liveness and readiness probes |
| healthzPort | int | `8080` | Port the HTTP server binds to; used for the container port and probes |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| image.repository | string | `"registry.k8s.io/dra-driver-cpu/dra-driver-cpu"` | Container image repository |
| image.tag | string | `""` | Image tag; defaults to `.Chart.AppVersion` when empty, which is set to the release tag at package time |
| imagePullSecrets | list | `[]` | List of image pull secrets |
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
