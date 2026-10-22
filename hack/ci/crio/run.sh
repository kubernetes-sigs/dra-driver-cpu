#!/usr/bin/env bash
# CRI-O E2E entrypoint for GitHub Actions and Prow. The cluster is built by the
# shared `make ci-kind-setup`; this script only adds what is CRI-O specific.
set -euo pipefail
cluster=${CRIO_CLUSTER_NAME:-dra-crio}
export KUBECONFIG="${KUBECONFIG:-/tmp/dra-crio-kubeconfig}"
export TAG="${TAG:-crio-ci}"
# The sysfs overlay supplies four CPUs and no reserved set.
export DRACPU_E2E_RESERVED_CPUS=""
artifacts=${ARTIFACTS:-/tmp/dra-crio-artifacts}
# Refuse to reuse a cluster: the EXIT handler owns and deletes this name.
if kind get clusters 2>/dev/null | grep -Fxq "$cluster"; then
	echo "Cluster $cluster already exists; choose another CRIO_CLUSTER_NAME" >&2
	exit 1
fi
mkdir -p "$artifacts"
finish() {
	result=${1:-$?}
	trap - EXIT
	# Nothing to collect if setup failed before the cluster existed.
	if ! kind get clusters 2>/dev/null | grep -Fxq "$cluster"; then
		exit "$result"
	fi
	kind export logs --name "$cluster" "$artifacts/kind" || true
	kubectl --request-timeout=15s get pods -A -o wide >"$artifacts/pods.txt" 2>&1 || true
	kubectl --request-timeout=15s get resourceslices -o yaml >"$artifacts/slices.yaml" 2>&1 || true
	kubectl --request-timeout=15s logs -n kube-system -l app=dracpu --all-containers --prefix >"$artifacts/driver.log" 2>&1 || true
	for node in $(kind get nodes --name "$cluster" 2>/dev/null); do
		docker exec "$node" journalctl -u crio --no-pager >"$artifacts/${node}-crio.log" 2>&1 || true
	done
	kind delete cluster --name "$cluster" || true
	exit "$result"
}
trap finish EXIT

# Pin kind for this lane; older kind cannot drive the 1.37 node image. Helm comes
# from the caller (setup-helm in GitHub Actions) or `make install-helm`.
make install-kind
PATH="$(pwd)/bin:$PATH"
export PATH

# Serial on purpose: on a 4-vCPU runner, building the node image and both Go
# images in parallel was slower overall and made the image import flaky.
make ci-kind-setup \
	KIND_NODE_RUNTIME=crio CLUSTER_NAME="$cluster" CI_SYSFS_OVERLAY=true \
	DRACPU_E2E_RESERVED_CPUS=""

# Fail rather than silently running containerd tests under a CRI-O job name.
kubectl wait --for=condition=Ready nodes --all --timeout=180s
kubectl get nodes -o json | python3 -c '
import json,sys
nodes=json.load(sys.stdin)["items"]
assert nodes
for node in nodes:
    version=node["status"]["nodeInfo"]["containerRuntimeVersion"]
    print(node["metadata"]["name"], version)
    assert version.startswith("cri-o://"), version
'
for node in $(kind get nodes --name "$cluster"); do
	docker exec "$node" test -S /var/run/nri/nri.sock
done

# CDI assignment, cgroup cpuset and affinity, shared-pool exclusion, and
# recovery across a driver restart (NRI reconciliation spec).
DRACPU_E2E_LABEL_FILTER=runtime-conformance make test-e2e 2>&1 | tee "$artifacts/e2e.log"
