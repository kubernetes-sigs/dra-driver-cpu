# Quickstart

This guide takes you from an empty cluster to a pod running on exclusive, pinned CPUs, with a
verification step after each stage. It requires:

1. A **Kubernetes 1.34+ cluster** (with `DRAConsumableCapacity` enabled).
1. A container runtime with **NRI and CDI enabled** (containerd 2.0+ or CRI-O 1.30+).
1. The **kubelet CPUManager disabled** (`cpuManagerPolicy: none`) on driver nodes.

See [Compatibility](installation.md#compatibility) for the more details.

## Create a test cluster

If you do not already have a compatible cluster, you can create a local [kind](https://kind.sigs.k8s.io/) cluster configured for DRA from a checkout of this repository:

```bash
make kind-cluster
```

## Install the driver

```bash
helm install dra-driver-cpu oci://registry.k8s.io/dra-driver-cpu/charts/dra-driver-cpu --version 0.2.0 -n kube-system
```

## Verify the driver is up

The chart installs a DaemonSet and the cluster-scoped `dra.cpu` DeviceClass — the name your
claims will reference:

```console
$ kubectl get pods -n kube-system -l app.kubernetes.io/name=dra-driver-cpu
NAME                   READY   STATUS    RESTARTS   AGE
dra-driver-cpu-ggr8h   1/1     Running   0          27s
dra-driver-cpu-krwf5   1/1     Running   0          27s
dra-driver-cpu-t55n7   1/1     Running   0          27s

$ kubectl get deviceclass
NAME      AGE
dra.cpu   27s
```

Each driver pod discovers its node's CPU topology and publishes it as `ResourceSlice`
objects:

```console
$ kubectl get resourceslices
NAME                                               NODE                           DRIVER    POOL                           AGE
00000-dra.cpu-dra-driver-cpu-control-plane-8dpvs   dra-driver-cpu-control-plane   dra.cpu   dra-driver-cpu-control-plane   18s
00000-dra.cpu-dra-driver-cpu-worker-ldrgd          dra-driver-cpu-worker          dra.cpu   dra-driver-cpu-worker          18s
00000-dra.cpu-dra-driver-cpu-worker2-4fgkd         dra-driver-cpu-worker2         dra.cpu   dra-driver-cpu-worker2         18s
```

Inspect one (`kubectl get resourceslices -o yaml`) to see the devices and their topology
attributes — see [Device Attributes and Selectors](device-attributes.md) for annotated samples.

## Run a pod on exclusive CPUs

Create a `ResourceClaim` requesting two exclusive CPUs and a pod that consumes it:

```yaml
apiVersion: resource.k8s.io/v1
kind: ResourceClaim
metadata:
  name: exclusive-cpus
spec:
  devices:
    requests:
    - name: cpus
      exactly:
        deviceClassName: dra.cpu
        capacity:
          requests:
            dra.cpu/cpu: "2"
---
apiVersion: v1
kind: Pod
metadata:
  name: pinned-pod
spec:
  containers:
  - name: app
    image: busybox:1.36
    command: ["sleep", "infinity"]
    resources:
      # Mirror the claim's CPU count so scheduler accounting stays correct;
      # see workload-requirements.md for the full rules.
      requests:
        cpu: "2"
      limits:
        cpu: "2"
      claims:
      - name: cpus
  resourceClaims:
  - name: cpus
    resourceClaimName: exclusive-cpus
```

Apply it, then check the claim was allocated and the pod is running:

```console
$ kubectl get resourceclaims
NAME             STATE                AGE
exclusive-cpus   allocated,reserved   12s

$ kubectl get pod pinned-pod
NAME         READY   STATUS    RESTARTS   AGE
pinned-pod   1/1     Running   0          12s
```

## Verify the pinning

The container's cpuset is restricted to the CPUs the driver allocated:

```console
$ kubectl exec pinned-pod -- grep Cpus_allowed_list /proc/self/status
Cpus_allowed_list:	0-1
```

The allocation is also recorded in the claim's status — which device the CPUs came from and
how much `dra.cpu/cpu` capacity the claim consumed:

```bash
kubectl get resourceclaim exclusive-cpus -o jsonpath='{.status.allocation.devices.results}'
```

The exact CPU IDs depend on your node's topology. Every container *without* a claim — in this
pod or any other — is confined to the shared pool of remaining CPUs, and the driver resizes
that pool automatically as claims come and go.

## Before production

- Read [Workload Configuration Requirements](workload-requirements.md) — how DRA claims
  interact with `resources.requests.cpu`, pod QoS, and scheduler accounting. Skipping this
  can double-count CPUs or land your pod in an unintended QoS class.
- Choose a [device exposure mode](configuration.md#driver-configuration) — `grouped`
  (default, scales well) vs `individual` (fine-grained selection, e.g. by core type).
- Reserve system CPUs with `reservedCPUs` so the driver never hands them out — see
  [Configuration](configuration.md).

## Next steps

- More runnable manifests:
  [grouped mode](https://raw.githubusercontent.com/kubernetes-sigs/dra-driver-cpu/v0.2.0/hack/examples/pod_with_resource_claim_grouped_mode.yaml) (default),
  [individual mode](https://raw.githubusercontent.com/kubernetes-sigs/dra-driver-cpu/v0.2.0/hack/examples/pod_with_resource_claim_individual_mode.yaml),
  [pod-level resources](https://raw.githubusercontent.com/kubernetes-sigs/dra-driver-cpu/v0.2.0/hack/examples/pod_with_pod_level_resources.yaml).
- Steer placement with CEL selectors over topology attributes — examples in
  [Device Attributes and Selectors](device-attributes.md).
- Coming from the kubelet CPUManager static policy? See the
  [option-by-option mapping](feature-support.md#matching-cpu-manager-options).
- Something not working? See [Troubleshooting & Diagnostics](troubleshooting.md).
