#!/usr/bin/env bash
# Load local images into CRI-O's containers/storage on every node of a kind
# cluster. `kind load` only populates containerd, which CRI-O does not read.
# Usage: load-images.sh <cluster> <image>...
set -euo pipefail
cluster=$1
shift
archive=$(mktemp)
trap 'rm -f "$archive"' EXIT
docker save -o "$archive" "$@"
for node in $(kind get nodes --name "$cluster"); do
	docker exec -i "$node" podman load <"$archive"
done
