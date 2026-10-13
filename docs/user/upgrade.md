# Upgrading

This document detail the upgrade actions that are specific to releases of
the CPU DRA driver. It complements the general [installation](installation.md)
and [configuration](configuration.md) documentation.

Upgrades must preserve the [singleton ownership
invariant](how-it-works.md#how-it-works).
An upgrade may temporarily leave a node without an active driver, but it must
not create overlapping active owners.

## Backward compatibility policy

The backward compatibility promise must be considered

- best-effort (we try hard not to break, but may happen) for all alpha-level APIs (v1alpha1, v1alpha2...)
- tentative (we will break only in exceptional circumstances) for all beta-level APIs (v1beta1, v1beta2...)
- effective (no backward incompatible changes) for all the GA APIs (v1, v2...)

The driver exposes configuration APIs in `api/`. The project strives to support
these APIs adhering to the kubernetes best practices and standards, including
backward compatibility, under the maturity graduation process described above.
The backward compatibility promise covers the Go API types and the API
YAML/JSON serialization format and semantics, subject to the import path exception below.

The backward compatibility promise does not cover command line flags.
While the project strives to cause minimal disruptions to the users, it reserves
the option to remove deprecated options after only a single release. In other words,
flags deprecated in 0.3.0 may be removed already in 0.4.0.
In some non-default cases, flags may have a longer promised lifetime; in that case,
the versions on which they will be removed will be clearly documented.

The golang package import paths may change as part of API package reorganizations the
project may do across releases. While such changes do break golang source compatibility
and require consumers to update imports, they do not, by themselves, change the
serialized API version or its compatibility guarantees.
The package relocations will be documented with migration instructions.
The package relocations will never be done in patch releases.
Example:

```
# before
opaqueapi "github.com/kubernetes-sigs/dra-driver-cpu/api/v1alpha1"
# after
allocationapi "github.com/kubernetes-sigs/dra-driver-cpu/api/allocation/v1alpha1"
```

The backward compatibility promise does not cover golang packages outside the api packages.
The project strives, but can't guarantee, backward compatible changes in the golang
packages. Package relocations, likewise golang api packages, can always happen except
in patch releases.
Public packages (not these in internal/...) must be considered to have best-effort
backward compatibility promise until documented otherwise.

## Upgrade from 0.3.0 to 0.4.0 (EARLY WARNING)

Before the driver enters beta stage, a batch of **breaking** changes are planned.
The idea is to coalesce together all the known backward incompatible changes in release 0.4.0.
We advice users wishing to prepare early to track the linked issues.

Anticipated breaking or possibly breaking changes are:

- breaking: memory support (https://github.com/kubernetes-sigs/dra-driver-cpu/issues/36)
  reason: requires repo and artifact rename; requires configuration file changes.
- breaking: driver identity change (https://github.com/kubernetes-sigs/dra-driver-cpu/issues/327)
  reason: identity change require existing object removal.
- possibly breaking: configuration format change (https://github.com/kubernetes-sigs/dra-driver-cpu/issues/302)
  reason: configuration overhaul. Some combination of options may need fixing.
- possibly breaking: driver persistence (https://github.com/kubernetes-sigs/dra-driver-cpu/issues/258)
  reason: implementation dependent.

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

If you use grouped mode with `groupBy: machine`, make sure all the claims use
exact or fistAvailable device request count to 1. Different request counts will
cause the driver to fail at claim preparation stage.
This constraint was previously implicit prior to 0.3.0 and it is now explicit.

#### Configuration: individual mode

Individual mode is deprecated. For scheduler-controlled selection of exact CPUs, follow
[Migrating from individual mode](opaque-cpuset-overrides.md#migrate-from-individual-mode).

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
