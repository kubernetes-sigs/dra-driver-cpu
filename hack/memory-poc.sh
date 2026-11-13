#!/usr/bin/env bash
# Copyright 2026 The Kubernetes Authors.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
if [[ ${1:-} == setup || ${1:-} == teardown ]]; then
    exec "$root/hack/poc/cluster.sh" "$1"
fi
kube=${KUBERNETES_ROOT:-${HOME}/go/src/k8s.io/kubernetes}
pin=308996eca7f441e1fff1e2984532873778d736de
if [[ $(git -C "$kube" rev-parse HEAD) != "$pin" ]]; then
    echo "Use Kubernetes checkout $pin (KUBERNETES_ROOT=$kube)." >&2
    exit 1
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GOTOOLCHAIN=${POC_GOTOOLCHAIN:-go1.27.2}
export GOWORK=$work/go.work
cd "$work"
go work init
go work use "$root"
for module in "$kube"/staging/src/k8s.io/*/go.mod; do
    go work use "$(dirname "$module")"
done
cd "$root"
case ${1:-build} in
    build) CGO_ENABLED=0 go build -o bin/dracpu ./cmd/dracpu; CGO_ENABLED=0 go build -o bin/dracputester ./test/image/dracputester ;;
    helpers) CGO_ENABLED=0 go build -o bin/pocsetup ./tools/pocsetup ;;
    check-setup) go test -count=1 ./internal/setup/... ;;
    images) docker build -f hack/poc/Dockerfile -t dra-cpu-poc:local . ;;
    unit)
        CGO_ENABLED=1 go test -race -count=1 ./pkg/... ./internal/... ./cmd/... ./test/image/dracputester/...
        CGO_ENABLED=1 go test -race -count=1 ./test/e2e -run '^TestPOCNUMANode$'
        ;;
    e2e)
        export KUBECONFIG=${POC_KUBECONFIG:-$root/bin/memory-poc.kubeconfig}
        export DRACPU_E2E_MEMORY_POC=true
        if [[ ${POC_ENABLE_MEMORY:-true} == false ]]; then export DRACPU_E2E_MEMORY_POC_CPU_ONLY=true; fi
        export DRACPU_E2E_TARGET_NODE=${DRACPU_E2E_TARGET_NODE:-${POC_CLUSTER_NAME:-dra-cpu-memory-poc}-worker}
        export DRACPU_E2E_TEST_IMAGE=dra-cpu-poc:local
        go test -count=1 -timeout=30m ./test/e2e -ginkgo.label-filter="${DRACPU_E2E_LABEL_FILTER:-memory-poc}"
        ;;
    compile-e2e) go test -run '^$' ./test/e2e ;;
    node-image) "${KIND:-$root/bin/kind}" build node-image --type source "$kube" --image dra-cpu-poc:k8s-${pin:0:12} ;;
    *) echo "Usage: $0 {build|helpers|images|check-setup|node-image|setup|e2e|compile-e2e|teardown}" >&2; exit 2 ;;
esac
