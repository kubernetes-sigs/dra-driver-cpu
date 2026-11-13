#!/usr/bin/env bash
# Copyright 2026 The Kubernetes Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
kind=${KIND:-$root/bin/kind}
helm=${HELM:-$root/bin/helm}
name=${POC_CLUSTER_NAME:-dra-cpu-memory-poc}
node_image=dra-cpu-poc:k8s-308996eca7f4
context=kind-$name
image=dra-cpu-poc:local
enable=${POC_ENABLE_MEMORY:-true}
if [[ $enable != true && $enable != false ]]; then
    echo 'POC_ENABLE_MEMORY must be true or false.' >&2; exit 1
fi
export KUBECONFIG=${POC_KUBECONFIG:-$root/bin/memory-poc.kubeconfig}
case ${1:-setup} in
    setup)
        # A separate kubeconfig avoids replacing the operator's active context.
        if "$kind" get clusters | grep -Fxq "$name"; then
            echo "Cluster $name already exists; use ./hack/memory-poc.sh teardown before setup." >&2
            exit 1
        fi
        test -x "$root/bin/pocsetup" || {
            echo 'Run ./hack/memory-poc.sh helpers before setup.' >&2; exit 1;
        }
        for tool in "$kind" "$helm" docker kubectl; do
            command -v "$tool" >/dev/null || { echo "Install $tool; see docs/user/memory-poc.md." >&2; exit 1; }
        done
        "$root/bin/pocsetup" preflight --memory="$enable"
        "$kind" create cluster --name "$name" --image "$node_image" --config "$root/hack/poc/kind.yaml" --kubeconfig "$KUBECONFIG"
        tmp=$(mktemp -d)
        trap 'rm -rf "$tmp"' EXIT
        for node in $("$kind" get nodes --name "$name"); do
            docker cp "$node:/etc/containerd/config.toml" "$tmp/config.toml"
            "$root/bin/pocsetup" runtime-config < "$tmp/config.toml" > "$tmp/patched.toml"
            docker cp "$tmp/patched.toml" "$node:/etc/containerd/config.toml"
            docker exec "$node" systemctl restart containerd
            docker exec "$node" containerd config dump | "$root/bin/pocsetup" check-runtime
            docker cp "$root/bin/pocsetup" "$node:/usr/local/bin/pocsetup"
            docker exec "$node" /usr/local/bin/pocsetup preflight --kind-node --memory="$enable"
            docker exec "$node" crictl info | "$root/bin/pocsetup" check-cri
        done
        kubectl --context "$context" label node "$name-worker" node-role.kubernetes.io/worker=
        "$kind" load docker-image --name "$name" "$image"
        "$helm" upgrade --install dracpu "$root/deployment/helm/dra-driver-cpu" --namespace kube-system \
            --kube-context "$context" \
            -f "$root/hack/poc/values.yaml" --set enableMemory="$enable"
        kubectl --context "$context" -n kube-system rollout status daemonset/dracpu --timeout=180s
        echo "POC ready. Follow docs/user/memory-poc.md to run examples/tests."
        ;;
    teardown) "$kind" delete cluster --name "$name" ;;
    *) echo "Usage: $0 {setup|teardown}" >&2; exit 2 ;;
esac
