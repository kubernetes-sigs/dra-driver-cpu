# dra-driver-cpu

Kubernetes Dynamic Resource Allocation (DRA) driver for CPU resources.
This repository implements a DRA driver that enables Kubernetes clusters to manage and assign exclusive CPUs to workloads using the DRA framework.
This driver provides an alternative to the [CPUManager](https://kubernetes.io/docs/tasks/administer-cluster/cpu-management-policies/) functionality implemented in the kubelet, offering additional benefits such as advanced topology selection through the rich DRA API and alignment with other DRA-managed resources (like GPUs and high-speed NICs).

> [!IMPORTANT]
> The kubelet's CPUManager implements assignment of exclusive CPUs to workloads. The CPUManager and this DRA driver are mutually incompatible and only
> one can be enabled at a time on any given node. See [Configuration](docs/user/configuration.md) for how to disable the CPUManager.

## Getting Started

Your cluster's container runtime must support NRI and CDI - see [Compatibility](docs/user/installation.md#compatibility).

The recommended way to install the driver is via the provided Helm chart:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

The [Quickstart](docs/user/quickstart.md) walks through installing the driver and running a pod on exclusive CPUs, with a verification step after each stage. See the [Helm chart README](deployment/helm/dra-driver-cpu/README.md) for the full list of configuration options, and
[Installation](docs/user/installation.md) for compatibility, upgrade, and uninstall details.

## Key Features

- **Topology-Aware CPU Discovery:** Discovers the node's full CPU topology by reading sysfs, including sockets, NUMA nodes, cores, SMT siblings, Last-Level Cache (LLC), core types (Performance/Efficiency), and optionally PCIe root locality.
- **Exclusive CPU Allocation:** Pods requesting CPUs via a `ResourceClaim` are pinned to exclusive, guaranteed CPUs enforced through CDI and NRI.
- **Shared Pool Management:** All other containers are dynamically confined to a shared pool made up of CPUs not exclusively assigned to any guaranteed container.
- **Two Device Exposure Modes:** `individual` mode exposes each CPU as a selectable device for fine-grained placement; `grouped` mode exposes larger aggregates (NUMA node/socket) as consumable capacity for better scalability on large systems.
- **CPU Manager Feature Parity:** Aims to match key kubelet CPUManager static policy options (e.g. `PreferAlignByUnCoreCache`, `StrictCPUReservation`) - see [Feature Support](docs/user/feature-support.md) for the full comparison.
- **Stateful Restarts:** Synchronizes with existing pods on restart by inspecting CDI-injected environment variables, rebuilding its allocation state without disrupting running workloads.

## Usage

### Topology-aware CPU allocation per workload

Each workload requests its own exclusive CPUs through a `ResourceClaim`. A CEL selector
constrains where the CPUs come from — for example, a specific NUMA node or socket — and the
selection is made per workload, not as a node-wide setting:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: cpus-on-numa0
spec:
  devices:
    requests:
    - name: cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "8"
        selectors:
        # Only allocate CPUs on NUMA node 0
        - cel:
            expression: device.attributes["resource.kubernetes.io"].numaNode == 0
---
apiVersion: v1
kind: Pod
metadata:
  name: pinned-pod
spec:
  containers:
  - name: app
    image: registry.k8s.io/pause:3.9
    resources:
      requests:
        cpu: "8"    # mirror the claim's CPU count
      limits:
        cpu: "8"
      claims:
      - name: cpus
  resourceClaims:
  - name: cpus
    resourceClaimName: cpus-on-numa0
```

The container referencing the claim is pinned to the CPUs allocated to that claim. The
mirrored `cpu` request is temporarily required to keep scheduler accounting correct — see
[Workload Configuration Requirements](docs/user/workload-requirements.md).

### CPU alignment with other DRA-managed resources

CPUs are allocated through the same DRA
machinery as GPUs and high-speed NICs (e.g. [DraNet](https://github.com/kubernetes-sigs/dranet)),
so a single claim can request CPUs together with other devices and keep them on the same
NUMA node or PCIe root via a `matchAttribute` constraint. For example, a distributed AI
training worker can ask for its data-loading CPUs, its GPU, and the NIC carrying collective
traffic to land on the same PCIe root:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: gpu-cpu-nic-claim
spec:
  devices:
    requests:
    - name: cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "8"
    - name: gpu
      exactly:
        deviceClassName: gpu.example.com
        count: 1
    - name: nic
      exactly:
        deviceClassName: dranet
        count: 1
    constraints:
    # Ensure CPUs, GPU, and NIC share the same PCIe root switch
    - requests: ["cpus", "gpu", "nic"]
      matchAttribute: resource.kubernetes.io/pcieRoot
---
apiVersion: v1
kind: Pod
metadata:
  name: training-worker-0
spec:
  containers:
  - name: trainer
    image: registry.example.com/trainer:latest
    resources:
      requests:
        cpu: "8"    # mirror the claim's CPU count
      limits:
        cpu: "8"
      claims:
      - name: devices
  resourceClaims:
  - name: devices
    resourceClaimName: gpu-cpu-nic-claim
```

PCIe root attributes are opt-in — see
[Feature Support](docs/user/feature-support.md#exposing-pcie-roots). CPUs and other
DRA-managed devices can also be aligned per NUMA node through the standard
`resource.kubernetes.io/numaNode` attribute. The driver-specific `dra.cpu/numaNodeID` and
`dra.net/numaNode` attributes remain available for compatibility. For all selectable
attributes and more example claims, see
[Device Attributes and Selectors](docs/user/device-attributes.md). Coming from the kubelet
CPU Manager? See the
[option-by-option mapping](docs/user/feature-support.md#matching-cpu-manager-options).

## How It Works

The driver runs as a single executable, deployed as a DaemonSet, combining a **DRA driver** control loop (topology discovery, ResourceSlice publication, CDI spec generation) and an **NRI plugin** (cgroup cpuset pinning and shared-pool management).

See [How it Works](docs/user/how-it-works.md) for the detailed architecture.

## Troubleshooting

If you run into problems, run the [`dracpu gatherinfo`](docs/user/troubleshooting.md) diagnostic tool and attach its output
when filing an issue — it collects the CPU topology and driver configuration needed to diagnose most problems quickly.

## Documentation

### User Documentation

- [Quickstart](docs/user/quickstart.md) - install, run a pod on exclusive CPUs, and verify each step.
- [Installation](docs/user/installation.md) - compatibility, runtime setup, security, upgrade, uninstall, and migration from `install.yaml`.
- [Configuration](docs/user/configuration.md) - the config file schema, command-line flags, and kubelet prerequisites.
- [How it Works](docs/user/how-it-works.md) - driver architecture, CDI, and NRI integration.
- [Feature Support](docs/user/feature-support.md) - supported/unsupported features.
- [Matching Kubelet CPU Manager Options](docs/user/feature-support.md#matching-cpu-manager-options) - kubelet cpumanager policy options and their driver equivalents.
- [Workload Configuration Requirements](docs/user/workload-requirements.md) - how to set pod/container CPU requests alongside DRA claims.
- [Custom Opaque CPUSet Allocation Overrides](docs/user/opaque-cpuset-overrides.md) - explicit core assignment for `groupBy: machine` mode.
- [Metrics](docs/user/metrics.md) - Prometheus metrics exposed by the driver.
- [Device Attributes and Selectors](docs/user/device-attributes.md) - selectable device attributes, CEL selector examples, and sample `ResourceSlice` output in each mode.
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
