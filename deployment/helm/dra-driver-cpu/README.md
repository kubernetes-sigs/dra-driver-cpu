# dra-driver-cpu Helm Chart

Kubernetes DRA driver for managing CPU resources with topology-aware allocation, exclusive CPU assignment, and shared CPU pool management via the Dynamic Resource Allocation framework.

## Installation

From a local checkout:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system
```

To override values at install time:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system \
  --set args.cpuDeviceMode=individual \
  --set args.reservedCPUs="0-1"
```

Parameters can be set at install time using `--set` or a custom values file:

```bash
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system --set args.logLevel=4
helm install dra-driver-cpu ./deployment/helm/dra-driver-cpu -n kube-system -f my-values.yaml
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| args.cpuDeviceMode | string | `"grouped"` | CPU exposure mode: `grouped` (expose NUMA nodes or sockets as devices) or `individual` (expose each CPU as a device) |
| args.groupBy | string | `"numanode"` | Grouping criteria when `cpuDeviceMode=grouped`: `numanode` or `socket` |
| args.hostnameOverride | string | `""` | Override the node name the driver registers under; omitted when empty |
| args.logLevel | int | `4` | Log verbosity level passed as `--v` |
| args.reservedCPUs | string | `""` | CPUs reserved for the OS and kubelet, excluded from DRA management (e.g. `"0-1"`); omitted when empty |
| fullnameOverride | string | `""` | Override the full release name |
| healthzPath | string | `"/healthz"` | Path for liveness and readiness probes |
| healthzPort | int | `8080` | Port the HTTP server binds to; used for the container port and probes |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy |
| image.repository | string | `"us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu"` | Container image repository |
| image.tag | string | `""` | Image tag; defaults to `.Chart.AppVersion` when empty, which is set to the release tag at package time |
| imagePullSecrets | list | `[]` | List of image pull secrets |
| nameOverride | string | `""` | Override the chart name |
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
