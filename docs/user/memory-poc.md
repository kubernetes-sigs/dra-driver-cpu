# CPU, memory, and hugepages POC: runbook

This is an experimental demonstration of one `dra.cpu` driver serving four
DeviceClasses: `dra.cpu`, `dra.memory`, `dra.hugepages-2m`, and
`dra.hugepages-1g`. It is not a production memory manager. The source contains
unit tests and an opt-in e2e suite; a successful build is **not** an e2e result.

Run every command below from the CPU driver's repository root. The setup is for
a **disposable Linux/amd64 host with local Docker**, not a shared cluster.
Nothing below should be pointed at a production node.

## 1. Prerequisites

- Linux/amd64 with cgroup v2, cpuset and hugetlb controllers, and 2 MiB
  hugetlb support. **1 GiB pages are optional.** Allow at least 8 GiB of
  host RAM; more is useful when building Kubernetes locally. Runtime allocation of 1 GiB pages may fail on a
  fragmented host even with enough free RAM.
- Docker running and accessible to your user. For rootless Docker, cpuset must
  be delegated; see the [e2e troubleshooting instructions](../../test/e2e/README.md#troubleshooting).
- Git, Go, `kubectl`, kind v0.32.0, and Helm 3.20.
  Go must be allowed to download the Go 1.27.2 toolchain and module dependencies.
- Swap disabled on the disposable host. The supplied kubelet configuration
  disables MemoryQoS and uses CPUManager `none` and MemoryManager `None`.

The POC uses this local Kubernetes source checkout:

```bash
export KUBERNETES_ROOT="$HOME/go/src/k8s.io/kubernetes"
git -C "$KUBERNETES_ROOT" rev-parse HEAD
# Required: 308996eca7f441e1fff1e2984532873778d736de
```

Use clean tracked source at that revision. Do not replace an existing checkout
with a hard reset. The setup helper and preflight implementation were copied
from `dra-driver-memory` revision `d6a6c17` into `internal/setup`, with source
attribution and small updates for containerd v3 and kind. No checkout, import, or
module dependency on `dra-driver-memory` is needed. The driver implementation
itself reuses concepts, not memory-driver code.

If kind is not installed:

```bash
GOBIN="$PWD/bin" go install sigs.k8s.io/kind@v0.32.0
```

By default the scripts use `bin/kind` and `bin/helm`. Set `KIND` or `HELM` to an
absolute executable path if installed elsewhere, for example:

```bash
export KIND="$(command -v kind)"
export HELM="$(command -v helm)"
```

## 2. Build and check without a cluster

```bash
./hack/memory-poc.sh build
./hack/memory-poc.sh helpers
./hack/memory-poc.sh check-setup
./hack/memory-poc.sh unit
./hack/memory-poc.sh compile-e2e
```

`build` uses the pinned checkout's staging modules in a temporary Go workspace.
It writes static `bin/dracpu` and `bin/dracputester` binaries. It does not change
`go.mod`, install workspace files in the repository, or modify Kubernetes source.

`helpers` builds `bin/pocsetup` from the local Go setup code. Its commands are:

- `runtime-config`: rewrite a containerd TOML stream, enabling NRI/CDI/hugetlb.
- `check-runtime`: validate a containerd TOML stream after restart.
- `preflight`: run the copied cgroup validator and additional kind checks.
- `hugepages`: provision pages from the supplied YAML, then verify actual counts.
- `check-cri`: validate runtime readiness from `crictl info` JSON.

`check-setup` runs the copied validator's tests plus tests for containerd v2/v3,
idempotency, preservation of unrelated runtime settings, delegation failures,
parent limits, and provisioning on a temporary fake sysfs. It does not contact
Docker or write host configuration. TOML and mountinfo parsers are the two extra
maintained dependencies needed by the copied setup tools; the driver does not
import the setup package.

## 3. Provision hugepages before creating the cluster

First record the current counts so you can restore them later:

```bash
for f in /sys/devices/system/node/node*/hugepages/hugepages-*/nr_hugepages; do
    printf '%s %s\n' "$f" "$(cat "$f")"
done > bin/memory-poc-hugepages.before
```

On this disposable host, provision sixty-four 2 MiB pages. The default file
does not request any 1 GiB pages:

```bash
sudo bin/pocsetup hugepages --config=hack/poc/hugepages.yaml
for f in /sys/devices/system/node/node*/hugepages/hugepages-*/nr_hugepages; do
    printf '%s: %s\n' "$f" "$(cat "$f")"
done
```

**This command changes host-wide hugepage reservations.** The copied helper
splits counts across NUMA nodes on a multi-node machine. Verify the actual counts:
the helper reads the counters back and fails if the kernel did not obtain the
requested pages. Its splitting algorithm requires contiguous NUMA node IDs;
provision externally on a different topology.
At least one NUMA node needs 2 MiB pages, ordinary memory, and at least two
allocatable CPUs for the CPU/memory/2 MiB composition tests. Neither setup nor
these tests require 1 GiB provisioning or a `hugetlb.1GB.max` cgroup file.

For the optional all-four-resource examples/tests, provision both sizes before
cluster creation instead:

```bash
sudo bin/pocsetup hugepages --config=hack/poc/hugepages-all.yaml
```

This requests two 1 GiB pages as well as the 2 MiB pages. Tests requiring 1 GiB
skip explicitly when the target node does not advertise sufficient capacity with
the required NUMA alignment. An advertised allocation that fails at runtime is a
test failure, not a skip.

If 1 GiB runtime provisioning fails, reserve those pages at boot using your
host's bootloader configuration, then recheck the per-node counts. Do not try to
solve fragmentation by giving the driver more capabilities.

The driver discovers a fixed inventory at startup. Changing page reservations
after setup requires deleting the demo workloads and recreating the cluster.

## 4. Build images and set up the dedicated cluster

```bash
./hack/memory-poc.sh node-image
./hack/memory-poc.sh images
./hack/memory-poc.sh setup
export KUBECONFIG="$PWD/bin/memory-poc.kubeconfig"
```

The node-image build can take time and writes Kubernetes build artifacts in its
checkout. The POC workload/driver image is `dra-cpu-poc:local` and contains the
binaries built in step 2. Re-run `build` and `images` after changing Go code.

`setup` creates `dra-cpu-memory-poc` with one control-plane node and one worker.
It enables the native-resource and consumable-capacity gates, uses the copied Go
containerd helper with config v2/v3 support, enables NRI/CDI/hugetlb handling,
restarts containerd inside the new nodes, loads the image, and installs the chart
on the worker only. It does **not** add privileges to the driver or provision
host pages. It refuses to overwrite an existing cluster with the same name.

The separate kubeconfig does not replace your usual active context. Keep it set
for all manual commands below. If setup fails part-way, inspect the failure,
run `./hack/memory-poc.sh teardown`, correct the prerequisite, and retry setup.

Verify publication:

```bash
kubectl get nodes
kubectl -n kube-system get pods -l app.kubernetes.io/name=dra-driver-cpu
kubectl get deviceclasses
kubectl get resourceslices -o yaml
```

Expect four classes. The driver's pools on the worker are its node name,
`<node>/memory`, and the provisioned hugepage pools. Without 1 GiB pages there is
no `<node>/hugepages-1g` pool/device; its class can still exist. Memory-family
capacity is in bytes. Hugepage class names use `2m`/`1g`; their native resource
mappings use Kubernetes names `hugepages-2Mi`/`hugepages-1Gi`.

### Node-setup lessons retained from dra-driver-memory

- Containerd must actually enable CDI/NRI and must set both
  `disable_hugetlb_controller` and `tolerate_missing_hugetlb_controller` to false.
  The helper checks the rewritten configuration, restarts the node runtime, and
  checks CRI readiness. For runtime-side evidence, inspect its startup journal:
  `docker exec dra-cpu-memory-poc-worker journalctl -u containerd --no-pager`.
- Check controllers **inside the kind node**, not just on the host. Rootless
  delegation and ancestor limits can otherwise make valid container adjustments
  ineffective. Setup checks node cgroup v2/controllers and the node-parent 2 MiB
  budget before deploying the driver. Optional 1 GiB cases still verify their
  actual limits and allocations at runtime.
- Reject `memory_hugetlb_accounting`: this demonstration keeps regular memory and
  hugepages in separate accounting pools, as the memory driver does.
- Container limits alone are insufficient if a pod/ancestor hugetlb limit is
  zero. The older memory driver had to manage pod cgroups directly; here the
  pinned Kubernetes native mappings supply pod-level hugepage budgets. Do not
  remove those mappings or mirror conventional requests. Successful mmap/touch
  tests validate the entire hierarchy, not merely a displayed container limit.
- Provision before kubelet/driver startup, verify actual per-node page counts,
  and do not advertise a shared kind host inventory through several workers.

## 5. Run the examples

Run examples **one at a time**, deleting each before proceeding; finished Pods
can retain their claims. All examples target `dra-cpu-memory-poc-worker` and use
`dra-cpu-poc:local`. For a custom cluster name, change that node selector first.

```bash
kubectl create namespace memory-demo
```

### 2 MiB hugepages only (recommended first)

This example consumes one 2 MiB page and no CPU, regular-memory, or 1 GiB claim:

```bash
kubectl -n memory-demo apply -f hack/examples/hugepages-2m.yaml
kubectl -n memory-demo wait --for=jsonpath='{.status.phase}'=Succeeded pod/hugepages-2m --timeout=180s
kubectl -n memory-demo logs hugepages-2m
kubectl -n memory-demo delete -f hack/examples/hugepages-2m.yaml --wait=true
```

Expect `huge2MMax: "2097152"` and `touched: ["hugepages-2m"]`. The probe actually
maps and touches a 2 MiB hugepage; it does not merely inspect the reported limit.
If the 1 GiB controller file is absent, `huge1GMax` is empty, not a made-up limit.

### CPU, memory, and 2 MiB pages in one aligned claim

This example checks composition and NUMA alignment without requesting 1 GiB pages:

```bash
kubectl -n memory-demo apply -f hack/examples/memory-aligned-2m.yaml
kubectl -n memory-demo wait --for=jsonpath='{.status.phase}'=Succeeded pod/memory-aligned-2m --timeout=180s
kubectl -n memory-demo logs memory-aligned-2m
kubectl -n memory-demo get pod memory-aligned-2m -o yaml
kubectl -n memory-demo delete -f hack/examples/memory-aligned-2m.yaml --wait=true
```

Expect one effective CPU, `memoryMax: "67108864"`, `huge2MMax: "2097152"`, and
`touched: ["memory", "hugepages-2m"]`. CPU affinity and limits are checked by the
probe. No 1 GiB cgroup limit is required or checked.

### Separate memory-family claims, composed in one container (requires 1 GiB)

```bash
kubectl -n memory-demo apply -f hack/examples/memory-separate.yaml
kubectl -n memory-demo wait --for=jsonpath='{.status.phase}'=Succeeded pod/memory-separate --timeout=180s
kubectl -n memory-demo logs memory-separate
kubectl -n memory-demo get resourceclaims -o yaml
kubectl -n memory-demo delete -f hack/examples/memory-separate.yaml --wait=true
```

The probe checks `memory.max=67108864`, `hugetlb.2MB.max=2097152`, and
`hugetlb.1GB.max=1073741824`. It touches ordinary memory and one page of each
hugetlb size. Its JSON output includes effective CPUs, memory nodes, limits,
and `touched: ["memory", "hugepages-2m", "hugepages-1g"]`. With no CPU claim it
uses the shared CPU pool. Different memory-node selections compose by union.

### One explicitly NUMA-aligned claim for all four resources (requires 1 GiB)

```bash
kubectl -n memory-demo apply -f hack/examples/memory-aligned.yaml
kubectl -n memory-demo wait --for=jsonpath='{.status.phase}'=Succeeded pod/memory-aligned --timeout=180s
kubectl -n memory-demo logs memory-aligned
kubectl -n memory-demo get pod memory-aligned -o yaml
kubectl -n memory-demo delete -f hack/examples/memory-aligned.yaml --wait=true
```

The claim uses the standard NUMA `matchAttribute` constraint. In addition to the
memory checks, the probe checks one effective CPU and matching process affinity.
The Pod's `status.nodeAllocatableResourceClaimStatuses` should contain the claim's
native quantities, without mirrored conventional Pod requests/limits.

Delete the example namespace when finished (including when using only the
2 MiB examples):

```bash
kubectl delete namespace memory-demo --wait=true
```

## 6. Run the e2e demonstration

After deleting the manual examples:

```bash
./hack/memory-poc.sh e2e
```

The helper supplies the dedicated kubeconfig, target worker, test image, and
`memory-poc` label filter. The serial suite covers each resource alone, separate
claims composed in a container, one aligned mixed claim, and disjoint requests
from a mixed claim consumed by two containers. All these composition patterns
have cases using only CPU, regular memory, and 2 MiB pages. Cases requiring
1 GiB select their prerequisites independently; their skips do not block the
2 MiB cases.

To select only the four 2 MiB-page cases:

```bash
DRACPU_E2E_LABEL_FILTER=memory-poc-2m ./hack/memory-poc.sh e2e
```

The split-container 2 MiB case assigns CPU/memory to one container and 2 MiB
pages to the other, checking exact budgets and shared/exclusive CPU separation.
The suite also checks native-resource status, effective affinity/limits, and
successful memory touches. Namespace cleanup is registered before
creating workloads. Missing 2 MiB capacity fails the relevant 2 MiB cases;
missing 1 GiB capacity explicitly skips only the cases that need it. A successful
2 MiB run does not claim 1 GiB or all-four-resource acceptance.

Request-scoped limits are important: Kubernetes currently aggregates native
quantities per claim rather than per selected request. This POC uses NRI to
apply each container's selected memory/hugepage budgets. The split-container
case checks that one container's unselected hugepage budget is zero.

For a separate CPU-only deployment, tear down first, then use:

```bash
./hack/memory-poc.sh teardown
export POC_ENABLE_MEMORY=false
export POC_CLUSTER_NAME=dra-cpu-memory-poc-cpu
export POC_KUBECONFIG="$PWD/bin/memory-poc-cpu.kubeconfig"
./hack/memory-poc.sh setup
./hack/memory-poc.sh e2e
./hack/memory-poc.sh teardown
unset POC_ENABLE_MEMORY POC_CLUSTER_NAME POC_KUBECONFIG
```

This deployment passes `--enable-memory=false` and creates only the CPU class.
The suite runs its CPU-only case and explicitly skips memory cases. This is a
CPU regression check, **not** a passing all-resource run.

## 7. Troubleshooting and cleanup

For a Pending or failed probe:

```bash
kubectl -n memory-demo describe pod memory-aligned
kubectl -n memory-demo logs memory-aligned
kubectl -n kube-system logs daemonset/dracpu --tail=200
kubectl get resourceclaims -A -o yaml
```

Use the actual test namespace/name for e2e failures; the fixture logs its
namespace. `kubectl describe` explains scheduling failures. A failed mmap can
mean missing/free pages or hugetlb permission: check the workload's group
against `vm.hugetlb_shm_group`. Adjust that setting only on the disposable host,
record its old value, and restore it afterward. No additional driver capability
is needed. A cgroup-controller or runtime setup error must be corrected before
rerunning the test; it is not evidence of working memory enforcement.

Delete the dedicated cluster:

```bash
./hack/memory-poc.sh teardown
```

Only after all demo workloads are gone, restore the recorded page counts:

```bash
while read -r file count; do
    printf '%s' "$count" | sudo tee "$file" >/dev/null
done < bin/memory-poc-hugepages.before
```

Do not reduce reservations still used by other workloads. The helper deliberately
does not restore host reservations automatically during cluster teardown.

## Limits of the demonstration

- A consumable budget is not a physical page reservation or protection from host
  memory pressure. Claim-only Pods do not become Guaranteed QoS Pods.
- One container has one `cpuset.mems`; union cannot enforce a different NUMA set
  for each memory type. Alignment is opt-in, not inferred from CPU allocation.
- Conventional and claim-based budgets for the same resource are not covered.
- Multi-NUMA union is covered by unit tests; the basic examples need only one
  NUMA node. The aligned e2e cases require CPU NUMA grouping.
- No provisioning in the driver, MemoryQoS, resize, hotplug, migration, durable
  recovery redesign, or arbitrary-failure guarantees are demonstrated.

See [the implementation notes](../dev/memory-poc.md) for the internal boundaries.
