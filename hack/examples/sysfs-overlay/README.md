# Sysfs overlay example

This example supplies a synthetic four-CPU topology. The ConfigMap owns the overlay data, while the values file uses `driverConfig.sysfsOverlay` plus the chart's generic extra volume mounts and volumes.

One environment where this is useful is a Kind cluster running in Docker Desktop for macOS, where the container-visible sysfs may not expose complete NUMA topology. The CPU and NUMA portions of this example work on Apple Silicon; PCIe-root discovery is currently limited to AMD64.

For example, with a local kind cluster, run:

```bash
export DRACPU_IMAGE_TAG=sysfs-overlay-example
export KIND_CLUSTER_NAME=dra-driver-cpu

make build-image TAG="${DRACPU_IMAGE_TAG}"
kind load docker-image \
  "dev.kind.local/ci/dra-driver-cpu:${DRACPU_IMAGE_TAG}" \
  --name "${KIND_CLUSTER_NAME}"

# Deploy the sysfs configmap
kubectl apply -n kube-system -f hack/examples/sysfs-overlay/configmap.yaml

# Deploy the dra-cpu-driver with the newly build version
helm upgrade --install dra-driver-cpu deployment/helm/dra-driver-cpu \
  --namespace kube-system \
  --set fullnameOverride=dracpu \
  --set image.repository=dev.kind.local/ci/dra-driver-cpu \
  --set image.tag="${DRACPU_IMAGE_TAG}" \
  --set image.pullPolicy=IfNotPresent \
  -f hack/examples/sysfs-overlay/values.yaml
```

The example exposes two NUMA-grouped devices with two CPUs each. It also defines
one PCIe root local to each NUMA node: `pci0000:00` for CPUs 0-1 and
`pci0001:80` for CPUs 2-3.

When changing the ConfigMap after deployment, restart the DaemonSet to reload the overlay:

```bash
kubectl rollout restart daemonset/dracpu -n kube-system
```
