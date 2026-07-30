# dra-driver-cpu

Kubernetes Dynamic Resource Allocation (DRA) driver for CPU resources.
This repository implements a DRA driver that enables Kubernetes clusters to manage and assign exclusive CPUs to workloads using the DRA framework.
This driver provides an alternative to the [CPUManager](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/) functionality implemented in the kubelet, offering additional benefits such as advanced topology selection through the rich DRA API and alignment with other DRA-managed resources (like GPUs and high-speed NICs).

**IMPORTANT:** The kubelet's CPUManager implements assignment of exclusive CPUs to workloads. The CPUManager and this DRA driver are mutually incompatible and only
one can be enabled at a time on any given node. See [Configuration](docs/user/configuration.md) for how to disable the CPUManager.

## Key Features

- **Topology-Aware CPU Discovery:** Discovers the node's full CPU topology by reading sysfs, including sockets, NUMA nodes, cores, SMT siblings, Last-Level Cache (LLC), core types (Performance/Efficiency), and optionally PCIe root locality.
- **Exclusive CPU Allocation:** Pods requesting CPUs via a `ResourceClaim` are pinned to exclusive, guaranteed CPUs enforced through CDI and NRI.
- **Shared Pool Management:** All other containers are dynamically confined to a shared pool made up of CPUs not exclusively assigned to any guaranteed container.
- **Two Device Exposure Modes:** `individual` mode exposes each CPU as a selectable device for fine-grained placement; `grouped` mode exposes larger aggregates (NUMA node/socket) as consumable capacity for better scalability on large systems.
- **CPU Manager Feature Parity:** Aims to match key kubelet CPUManager static policy options (e.g. `PreferAlignByUnCoreCache`, `StrictCPUReservation`) - see [Feature Support](docs/user/feature-support.md) for the full comparison.
- **Stateful Restarts:** Synchronizes with existing pods on restart by inspecting CDI-injected environment variables, rebuilding its allocation state without disrupting running workloads.

## How It Works

The driver runs as a single executable, deployed as a DaemonSet, that implements two core interfaces working together on each node:

- The **DRA driver** control loop discovers CPU topology, publishes `ResourceSlice` objects to the API server, and handles `ResourceClaim` allocation requests, writing the result as a [CDI](https://github.com/cncf-tags/container-device-interface) spec.
- An **NRI plugin** reads the CDI-injected cpuset for guaranteed containers and pins them via the cgroup cpuset controller, while dynamically managing the shared pool for everything else.

See [How it Works](docs/user/how-it-works.md) for the detailed architecture, and [Example ResourceSlices](docs/user/resourceslices-examples.md) for sample driver output.

## Getting Started

Your cluster's container runtime must support NRI and CDI - see [Prerequisites](docs/user/prerequisites.md).

The recommended way to install the driver is via the provided Helm chart:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

See the [Helm chart README](deployment/helm/dra-driver-cpu/README.md) for the full list of configuration options, and
[Getting Started](docs/user/installation.md) for cluster setup, migrating from the deprecated manifest-based installation, and example workloads.

If you run into problems, run the [`dracpu gatherinfo`](docs/user/troubleshooting.md) diagnostic tool and attach its output
when filing an issue — it collects the CPU topology and driver configuration needed to diagnose most problems quickly.

## Documentation

### User Documentation

- [Prerequisites](docs/user/prerequisites.md) - container runtime (NRI/CDI) requirements.
- [Getting Started](docs/user/installation.md) - installation, migration from `install.yaml`, and example usage.
- [Configuration](docs/user/configuration.md) - kubelet prerequisites, command-line flags, and the Helm `driverConfig` file schema.
- [How it Works](docs/user/how-it-works.md) - driver architecture, CDI, and NRI integration.
- [Feature Support](docs/user/feature-support.md) - supported/unsupported features and [CPU Manager Options mapping](docs/user/cpu-manager-mapping.md).
- [Matching CPU Manager Options](docs/user/cpu-manager-mapping.md) - kubelet cpumanager policy options and their driver equivalents.
- [Workload Configuration Requirements](docs/user/workload-requirements.md) - how to set pod/container CPU requests alongside DRA claims.
- [Custom Opaque CPUSet Allocation Overrides](docs/user/opaque-cpuset-overrides.md) - explicit core assignment for `--group-by=machine` mode.
- [Metrics](docs/user/metrics.md) - Prometheus metrics exposed by the driver.
- [Example ResourceSlices](docs/user/resourceslices-examples.md) - sample `ResourceSlice` output in each mode.
- [Troubleshooting & Diagnostics](docs/user/troubleshooting.md) - the `dracpu gatherinfo` diagnostic tool.

### Developer Documentation

- [Testing](docs/dev/testing.md) - running unit/E2E tests and testing local changes in a Kind cluster.
- [Linting](docs/dev/linting.md) - running and auto-fixing lint issues.
- [Logging Guidelines](docs/dev/logging.md)
- [Configuration Guidelines](docs/dev/configuration.md) - about adding more tunables to the driver
- [Deep dive: PCI/PCIe root buses on Linux](docs/dev/pci-bus-linux-sysfs.md)
- [Deep dive: Linux topology reporting](docs/dev/topology-linux-sysfs.md)

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).
Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).

You can reach the maintainers of this project at:

- [Slack](https://slack.k8s.io/) - preferred channels: #sig-node #wg-device-management
- [Mailing List](https://groups.google.com/a/kubernetes.io/g/dev)
