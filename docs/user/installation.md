# Getting Started

## Installation

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

For environments with incomplete or synthetic sysfs topology, e.g. Docker Desktop for macOS see the [sysfs overlay example](../../hack/examples/sysfs-overlay/README.md). It demonstrates how to supply an overlay through `driverConfig.sysfsOverlay`, volume mounts, and volumes.

## Example Usage

The driver supports two modes of operation. Each mode has a complete example manifest that includes both the ResourceClaim(s) and a sample Pod. The ResourceClaim requests a specific number of exclusive CPUs from the driver, and is referenced in the Pod spec to receive the allocated CPUs.

### Grouped Mode (default)

In grouped mode, CPUs are requested as a consumable capacity from a device group (e.g., NUMA node or socket). The `dra.cpu/cpu` capacity must be a positive whole number of logical CPUs; fractional, zero, and negative quantities are rejected during claim preparation. This example requests 10 CPUs.

- `kubectl apply -f hack/examples/pod_with_resource_claim_grouped_mode.yaml`

### Individual Mode

In individual mode, specific CPU devices are requested by count, allowing for fine-grained control over CPU selection. This example includes two ResourceClaims requesting 4 and 6 CPUs respectively, used by a Pod with multiple containers.

- `kubectl apply -f hack/examples/pod_with_resource_claim_individual_mode.yaml`

See [Example ResourceSlices](resourceslices-examples.md) for sample `ResourceSlice` output in each mode.

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
