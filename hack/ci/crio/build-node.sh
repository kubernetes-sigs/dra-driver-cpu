#!/usr/bin/env bash
# Build the CRI-O kind node image. Versions come from the Makefile; run it via
# `make ensure-kind-node-image KIND_NODE_RUNTIME=crio`.
set -euo pipefail
: "${NODE_IMAGE:?set by the Makefile (CRIO_BASE_NODE_IMAGE)}"
: "${CRIO_MINOR:?set by the Makefile}"
: "${CRIO_PACKAGE_VERSION:?set by the Makefile}"
: "${CRIO_NODE_IMAGE:?set by the Makefile}"
# The tag carries the versions; set CRIO_NODE_FORCE_BUILD=true after editing the
# Dockerfile or crio.conf locally.
if [[ ${CRIO_NODE_FORCE_BUILD:-false} != true ]] && [[ -n $(docker images -q "$CRIO_NODE_IMAGE" 2>/dev/null) ]]; then
	echo "Using existing $CRIO_NODE_IMAGE"
	exit 0
fi
builder="dra-crio-builder-$$"
cleanup() { docker rm -f "$builder" >/dev/null 2>&1 || true; }
trap cleanup EXIT
docker build --build-arg NODE_IMAGE="$NODE_IMAGE" --build-arg CRIO_MINOR="$CRIO_MINOR" \
	--build-arg CRIO_PACKAGE_VERSION="$CRIO_PACKAGE_VERSION" \
	-t "${CRIO_NODE_IMAGE}-build" hack/ci/crio
# Import kind's preloaded control-plane/CNI images from containerd into CRI-O's store.
docker run --privileged -d --name "$builder" --entrypoint sleep "${CRIO_NODE_IMAGE}-build" infinity
docker exec -d "$builder" containerd
for _ in $(seq 1 30); do
	if docker exec "$builder" ctr -n k8s.io images list -q >/dev/null 2>&1; then break; fi
	sleep 1
done
# Export to a file rather than piping: under CI load a ctr|podman pipe was cut
# mid-stream. Retry once.
docker exec "$builder" bash -euc '
  mapfile -t images < <(ctr -n k8s.io images list -q | grep -E "^(docker.io/|registry.k8s.io/)")
  ((${#images[@]} > 0))
  archive=$(mktemp /var/tmp/kind-image-XXXXXX.tar)
  for image in "${images[@]}"; do
    for attempt in 1 2; do
      if ctr -n k8s.io images export "$archive" "$image" && podman load -q -i "$archive"; then
        break
      fi
      if ((attempt == 2)); then echo "failed to import $image" >&2; exit 1; fi
      echo "retrying import of $image" >&2
    done
  done
  rm -f "$archive"
'
docker exec "$builder" crio --version
docker commit --change 'ENTRYPOINT ["/usr/local/bin/entrypoint", "/sbin/init"]' "$builder" "$CRIO_NODE_IMAGE"
