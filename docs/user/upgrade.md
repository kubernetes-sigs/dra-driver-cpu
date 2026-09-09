# Upgrading

This document detail the upgrade actions that are specific to releases of
the CPU DRA driver. It complements the general [installation](installation.md)
and [configuration](configuration.md) documentation.

## Upgrade from 0.2.0 to 0.3.0

### Action required

#### Helm

Upgrade the existing Helm release in place. Do not uninstall the release
first: uninstalling removes the driver DaemonSet and stops CPU allocation and
shared-pool reconciliation on every node.

Example command:

```bash
helm upgrade dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu -n kube-system
```

#### Configuration: grouped mode

If you use grouped mode with `groupBy: machine`, set the allocator explicitly
to `external` as part of the upgrade:

```yaml
driverConfig:
  cpuDeviceMode: grouped
  groupBy: machine
  allocator: external
```

This requirement was implicit prior to 0.3.0 and it is now explicit.
Failing to set the `external` allocator will cause the driver to fail to startup.

### Action recommended

#### Previous installations using `install.yaml`

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
