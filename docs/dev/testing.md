# Testing

## Running the tests

### Unit Tests

To run the node-local unit tests, run:

```bash
make test-unit
```

### Chart render tests

To check what the Helm chart renders, without a cluster, run:

```bash
make helm-render-check
```

`hack/ci/helm-render-check.sh` renders each case and hands the DaemonSet to the
validator in `test/render`. It needs Helm and the project's Go toolchain, but no
cluster. The cluster-side counterpart is the `helm e2e` workflow, which installs
the chart against both the default and a relocated kubelet root.

With no cluster to ask, Helm renders against its own built-in Kubernetes version,
which in some releases is below the chart's `kubeVersion` range and refuses the chart
outright. The script therefore renders at the oldest stable release that range covers.
Set `HELM_KUBE_VERSION` to render at a different one.

### E2E Tests

To run the e2e tests against a custom-setup, special-purpose kind cluster, just run:

```bash
make test-e2e-kind
```

Set the environment variable `DRACPU_E2E_VERBOSE` to 1 for more output:

```bash
DRACPU_E2E_VERBOSE=1 make test-e2e-kind
```

In some cases, you may need to set explicitly the KUBECONFIG path:

```bash
KUBECONFIG=${HOME}/.kube/config DRACPU_E2E_VERBOSE=1 make test-e2e-kind
```

The `test-e2e-kind` will exercise the same flows which are run on the project CI.
The full documentation for all the supported environment variables is found in the [tests README](../../test/e2e/README.md).

**NOTE:** the custom-setup kind cluster is _not_ automatically torn down once the tests terminate.
**NOTE:** if you want to run the tests again on the existing kind cluster, just use `make test-e2e`. Please see `make help` for more details.

## Testing Local Changes in a Kind Cluster

The simplest way to build the driver from source and deploy it into a new Kind cluster is using:

```bash
make ci-kind-setup
```

This command builds the driver image, creates a Kind cluster, loads the image, and installs the driver manifests in `grouped` mode (the default).

To install the driver in `individual` mode instead, use:

```bash
DRACPU_E2E_CPU_DEVICE_MODE=individual make ci-kind-setup
```

To clean up the environment when finished, run:

```bash
make delete-kind-cluster
```
