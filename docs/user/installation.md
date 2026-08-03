# Installation

The [Quickstart](quickstart.md) walks through an install with a
verification step after each stage, ending with a pod running on exclusive CPUs. This page
is the reference: compatibility, runtime setup, security, upgrade, uninstall, and migration.

## Compatibility

| Requirement       | Minimum                                                                                                                   |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------- |
| Kubernetes        | **1.34** (DRA `resource.k8s.io/v1` is GA and enabled by default)                                                          |
| Container runtime | containerd **2.0** or CRI-O **1.30** (NRI and CDI enabled by default)                                                     |
| Kubelet           | CPUManager disabled: `cpuManagerPolicy: none` — see [Configuration](configuration.md#kubelet-configuration-prerequisites) |

Optional features need additional cluster feature gates:

| Feature                                                     | Kubernetes feature gate                                |
| ----------------------------------------------------------- | ------------------------------------------------------ |
| `grouped` device mode (the default) on Kubernetes 1.34/1.35 | `DRAConsumableCapacity` (enabled by default from 1.36) |
| PCIe root attributes (`--expose-pcie-roots`)                | `DRAListTypeAttributes`                                |

The driver is Linux-only and needs node-level privileges (host networking, the `NET_ADMIN`
and `SYS_ADMIN` capabilities, and NRI socket, CDI directory, and kubelet plugin directory
access) — see [Security considerations](#security-considerations).

## Installing with Helm

If needed, create a kind cluster. We have one in the repo, if needed, that
can be deployed as follows:

```bash
make kind-cluster
```

The recommended way to install the driver is via the provided Helm chart:

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

See the [Helm chart README](../../deployment/helm/dra-driver-cpu/README.md) for the full list of configuration options.

Besides the driver DaemonSet, the chart installs the cluster-scoped `dra.cpu` `DeviceClass`
that workload claims reference — verify it with `kubectl get deviceclass`.

For environments with incomplete or synthetic sysfs topology, e.g. Docker Desktop for macOS see the [sysfs overlay example](../../hack/examples/sysfs-overlay/README.md). It demonstrates how to supply an overlay through `driverConfig.sysfsOverlay`, volume mounts, and volumes.

## Container runtime setup

The driver relies on [NRI (Node Resource Interface)](https://github.com/containerd/nri) to pin containers to their
allocated CPUs, and on [CDI (Container Device Interface)](https://github.com/cncf-tags/container-device-interface) to
inject the allocated cpuset into the container environment.

### Minimum Runtime Requirements

Both NRI and CDI are enabled by default in modern container runtimes:

| Runtime    | NRI enabled by default | CDI enabled by default |
| ---------- | ---------------------- | ---------------------- |
| containerd | 2.0+                   | 2.0+                   |
| CRI-O      | 1.30+                  | always                 |

Both runtimes also ship with the following CDI spec directories configured by default:

```toml
cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

No manual runtime configuration is needed if you are running one of the versions above or newer.

### Manual Configuration for Older Runtimes

If you are running an older version of containerd (pre-2.0), you need to manually enable CDI and NRI in the containerd
configuration (typically `/etc/containerd/config.toml`) and restart containerd.

Enable CDI:

```toml
[plugins."io.containerd.grpc.v1.cri"]
  enable_cdi = true
  cdi_spec_dirs = ["/etc/cdi", "/var/run/cdi"]
```

Enable NRI:

```toml
[plugins."io.containerd.nri.v1.nri"]
  disable = false
  disable_connections = false
  plugin_config_path = "/etc/nri/conf.d"
  plugin_path = "/opt/nri/plugins"
  plugin_registration_timeout = "5s"
  plugin_request_timeout = "5s"
  socket_path = "/var/run/nri/nri.sock"
```

After editing the config, restart containerd:

```bash
systemctl restart containerd
```

## Security considerations

The driver needs node-level privileges. It runs as a DaemonSet with `hostNetwork: true` and
the `NET_ADMIN` and `SYS_ADMIN` capabilities, with hostPath
mounts for the NRI socket (`/var/run/nri`), the CDI spec directory (`/var/run/cdi`), and the
kubelet plugin directories.

## Upgrading

Upgrade with Helm:

```bash
helm upgrade dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

## Uninstalling

```bash
helm uninstall dra-driver-cpu -n kube-system
```

Pods that are running keep their current cpusets, but nothing manages pinning or the shared
pool anymore; delete or reschedule claim-bearing pods afterwards.

## Installation via rendered manifest (deprecated)

> **Deprecated:** Manifest-based installation is deprecated in favor of the Helm chart and will be removed in a future release.
> New users should use the Helm-based installation above.

```bash
make manifests
kubectl apply -f dist/helm-manifest.yaml
```

## Migrating from install.yaml to Helm

`install.yaml` was the manifest used to install the driver in the `0.1.0` release and is now
obsolete. It has since been replaced by the rendered manifest above and, preferably, the Helm
chart. If you still have a cluster running the `install.yaml`-based installation, use the steps
below to migrate to the Helm chart.

Because the DaemonSet label selectors differ between `install.yaml` (`app: dracpu`) and the Helm chart
(`app.kubernetes.io/name`, `app.kubernetes.io/instance`), and DaemonSet selectors are immutable, an
in-place migration is not possible. The only practical migration path is a delete and reinstall:

```bash
# Step 1: remove the legacy manifest-managed resources
# (use the same manifest file that was originally applied)
kubectl delete -f <legacy-manifest>.yaml

# Step 2: install the Helm-managed release
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

**Disruption:** Deleting the DaemonSet terminates the driver pods on all nodes simultaneously. During
the migration window, no new CPU allocations can be made and the shared-pool cpuset updates stop.
Existing workloads are not evicted and their CPUs should remain. Once the new DaemonSet is scheduled
and the driver pods are running, the driver should recover its state.
