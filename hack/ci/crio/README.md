# CRI-O E2E

This lane runs the E2E suite against kind nodes that use CRI-O instead of
containerd. It reuses the containerd lane end to end: `make ci-kind-setup` builds
the images and the cluster, and `make test-e2e` runs the specs. Only the parts
that are CRI-O specific live in this directory.

`hack/ci/crio/run.sh` is the single entrypoint for both GitHub Actions and Prow
(after `runner.sh` initializes Docker-in-Docker). Once the Prow job is
registered in test-infra, remove the `pull_request` trigger from
`.github/workflows/crio-e2e.yaml` to avoid duplicate runs.

## What differs from the containerd lane

All of it is selected by `KIND_NODE_RUNTIME=crio`:

- **Node image**: `build-node.sh` layers CRI-O onto the official kind node image
  (`CRIO_BASE_NODE_IMAGE`, pinned by digest) and imports kind's preloaded images
  into CRI-O's containers/storage. Kubernetes and CRI-O are not compiled.
- **Cluster config**: `kind-patch.yaml` is appended to `hack/ci/kind-ci.yaml`, so
  only the CRI socket differs between the lanes.
- **Image loading**: `load-images.sh` uses Podman, because `kind load` populates
  containerd only.
- **Runtime config**: `crio.conf` enables CDI (`/var/run/cdi`) and NRI
  (`/var/run/nri/nri.sock`).

`run.sh` also pins kind (`KIND_VERSION`), gives the driver a deterministic
four-CPU topology through the sysfs overlay (`CI_SYSFS_OVERLAY=true`), and
refuses to run unless every node reports `cri-o://`. Helm comes from `PATH`, as in the
containerd lane; GitHub Actions installs the same v3.20.1 as `make install-helm`.

## Versions

Set once in the `Makefile`: `KIND_K8S_VERSION`, `KIND_VERSION`,
`CRIO_BASE_NODE_IMAGE`, `CRIO_MINOR`, `CRIO_PACKAGE_VERSION`. Bump the node
image digest and the CRI-O packages together. This lane does not establish
compatibility with the oldest CRI-O version mentioned in the user documentation.

## Coverage

It runs specs labeled `runtime-conformance`, currently the NRI reconciliation
spec: CDI-injected CPUs, cgroup cpuset and affinity agree; a shared pod is kept
off the exclusive CPUs; and both survive a driver and NRI restart. The overlay
supplies topology, so this does not qualify physical NUMA/PCIe placement or a
CRI-O service restart.

## Running locally

Requirements: rootful Docker on a Linux host whose sysfs exposes NUMA topology
(as GitHub-hosted runners do), at least four CPUs numbered 0-3, Go, kubectl and
Python 3. Docker Desktop is not supported: the spec's CPU discovery pod reads
the host sysfs, which the overlay does not cover, and finds no CPUs there.

```bash
KUBECONFIG=/tmp/dra-crio-kubeconfig hack/ci/crio/run.sh
```

The node image is reused when its tag already exists locally; set
`CRIO_NODE_FORCE_BUILD=true` after editing the Dockerfile or `crio.conf`. The
script removes only its own cluster (`dra-crio` by default); set
`CRIO_CLUSTER_NAME` and `KUBECONFIG` to unused values for concurrent runs.
Diagnostics, including CRI-O journals and E2E output, go to `ARTIFACTS`
(default `/tmp/dra-crio-artifacts`) on success or failure.
