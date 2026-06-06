# dracpu-gatherinfo

## Overview

`dracpu-gatherinfo` is a node-local diagnostic tool for collecting CPU information from the host where it runs. The command entrypoint is thin; the collection logic lives in `internal/gatherinfo` and writes a YAML report with a top-level `layoutVersion`.

This first iteration intentionally keeps the scope small:

- `cpuDetails`
- `driverConfig`

It does not collect Kubernetes control-plane objects and it does not emit the experimental hierarchical topology view.

## Usage

```bash
./dracpu-gatherinfo
```

By default the tool writes a temporary YAML file, prints the final path, and exits.

To place the output under a specific directory:

```bash
./dracpu-gatherinfo --output-dir=/var/log/dracpu-debug
```

In that case the tool writes one file per invocation under `/var/log/dracpu-debug`.

To stream the YAML report to standard output instead of creating a file:

```bash
./dracpu-gatherinfo --stdout > node-a.yaml
```

`--stdout` is intended for remote collection flows such as `kubectl exec`. It cannot be combined with `--output-dir`.

## Output Format

Example file:

```yaml
---
toolVersion:
  goVersion: go1.26.0
  vcsRevision: abcdef1234567890
  vcsTime: "2026-05-29T03:00:00Z"
layoutVersion: v1
cpuDetails:
  topology:
    numCPUs: 16
    numCores: 8
    numUncoreCache: 1
    numSockets: 1
    numNUMANodes: 1
    smtEnabled: true
  cpus:
    - cpuID: 0
      coreID: 0
      socketID: 0
      clusterID: -1
      numaNodeID: 0
      numaNodeCPUSet: 0-15
      sibling: 8
      coreType: standard
      uncoreCacheID: 0
driverConfig:
  cpuDeviceMode: grouped
  groupBy: numanode
  bindAddress: :8080
  reservedCPUs: 0-3
```

The report records:

- which binary produced the data
- which report layout the consumer should expect through the top-level `layoutVersion`
- the flat CPU inventory from `pkg/cpuinfo`
- the effective driver-side settings for the same node

When the driver process is running in the same container, `driverConfig` is derived from the driver's actual command line and falls back to defaults only when that information is unavailable.

## Integration With Bug Reports

1. Run `dracpu-gatherinfo` on the affected node.
1. Attach the generated `.yaml` file.
1. Include relevant driver logs from the same node.

## Collecting From a Cluster

The tool is node-local by design. To collect data from every node in a cluster, run it once per driver pod and save each YAML stream using the pod's node name.

Example:

```bash
namespace=dra-driver-cpu
selector='app.kubernetes.io/name=dra-driver-cpu'
outdir=./dracpu-gatherinfo

mkdir -p "${outdir}"

for pod in $(kubectl get pods -n "${namespace}" -l "${selector}" -o name); do
  node=$(kubectl get -n "${namespace}" "${pod}" -o jsonpath='{.spec.nodeName}')
  kubectl exec -n "${namespace}" "${pod}" -- /dracpu-gatherinfo --stdout > "${outdir}/${node}.yaml"
done
```

If you want to keep multiple node reports in a single stream, concatenate them as YAML documents:

```bash
for pod in $(kubectl get pods -n "${namespace}" -l "${selector}" -o name); do
  kubectl exec -n "${namespace}" "${pod}" -- /dracpu-gatherinfo --stdout >> all-nodes.yaml
done
```

Each invocation starts with `---`, so the combined file remains a valid multi-document YAML stream.
