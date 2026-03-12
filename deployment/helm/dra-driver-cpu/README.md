# dra-driver-cpu Helm Chart

Deploys the [dra-driver-cpu](https://github.com/kubernetes-sigs/dra-driver-cpu) DaemonSet — a Kubernetes Dynamic Resource Allocation (DRA) driver for managing CPU resources.

## Installation

```bash
helm install dra-driver-cpu ./dra-driver-cpu
```

To override values at install time:

```bash
helm install dra-driver-cpu ./dra-driver-cpu \
  --set args.cpuDeviceMode=individual \
  --set args.reservedCPUs="0-1"
```

## Values

| Parameter | Description | Default |
|---|---|---|
| `image.repository` | Container image repository | `us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu` |
| `image.tag` | Image tag (falls back to `Chart.AppVersion` if empty) | `latest` |
| `image.pullPolicy` | Image pull policy | `Always` |
| `resources.requests.cpu` | CPU resource request | `100m` |
| `resources.requests.memory` | Memory resource request | `50Mi` |
| `tolerations` | Node tolerations | control-plane taint tolerated |
| `args.logLevel` | Log verbosity (`--v`) | `4` |
| `args.cpuDeviceMode` | CPU exposure mode: `grouped` or `individual` | `grouped` |
| `args.reservedCPUs` | CPUs reserved for system/kubelet (e.g. `0-1`). Omitted if empty. | `""` |

## CPU Device Modes

**`grouped` (default)** — exposes CPUs as consumable capacity within a group (NUMA node or socket). Better scalability; suited for most workloads.

**`individual`** — exposes each CPU as a separate device. Fine-grained control; suited for HPC or performance-critical workloads.

## Reserving CPUs

Use `args.reservedCPUs` to exclude CPUs from DRA allocation, matching the kubelet's `reservedSystemCPUs` setting. The value should equal the sum of the kubelet's `kubeReserved` and `systemReserved` CPU counts.

```bash
helm install dra-driver-cpu ./dra-driver-cpu --set args.reservedCPUs="0-1"
```

## Uninstallation

```bash
helm uninstall dra-driver-cpu
```

