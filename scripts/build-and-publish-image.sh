#!/usr/bin/env bash
# Copyright The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
set -euo pipefail

REPO_ROOT=$(git rev-parse --show-toplevel)
cd "$REPO_ROOT"

if [[ -z ${IMG_PREFIX:-} ]]; then
	echo "IMG_PREFIX is not set"
	exit 1
fi

# Prow's image-builder passes the triggering ref as _PULL_BASE_REF, mapped to
# BUILD_REF by cloudbuild.yaml. TAG_NAME is only set for native tag triggers.
# Use the release automation's version format; a Git tag at HEAD alone does
# not distinguish a tag-triggered build from a branch build at the same commit.
release_tag=${BUILD_REF:-${TAG_NAME:-}}
if [[ ${release_tag} =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
	IMG_TAG=${release_tag}
else
	release_tag=""
fi

if [[ -z ${IMG_TAG:-} ]]; then
	# Use a tag if the current commit is a tag, otherwise use a date+git-hash tag.
	if git describe --exact-match --tags HEAD >/dev/null 2>&1; then
		IMG_TAG=$(git describe --exact-match --tags HEAD)
	else
		IMG_TAG="$(date +v%Y%m%d)-$(git rev-parse --short HEAD)"
	fi
fi
echo "Using IMG_TAG=${IMG_TAG}"

CHART_VERSION="${IMG_TAG#v}"
if [[ -z ${CHART_VERSION} ]]; then
	echo "CHART_VERSION is empty (IMG_TAG=${IMG_TAG})"
	exit 1
fi
echo "Using CHART_VERSION=${CHART_VERSION}"

# Pass the Cloud Build variables to the Makefile and enable multi-arch.
# REGISTRY must be set explicitly: the Makefile default is the read-only
# production registry, and the Makefile appends /dra-driver-cpu/dra-driver-cpu.
make push-image \
	REGISTRY="${IMG_PREFIX%/dra-driver-cpu}" \
	TAG="${IMG_TAG}" \
	PLATFORMS="linux/amd64,linux/arm64"

# Verify the image actually landed in the registry.
docker buildx imagetools inspect "${IMG_PREFIX}/dra-driver-cpu:${IMG_TAG}" >/dev/null

# Only publish the Helm chart for builds triggered by a release tag.
if [[ -n ${release_tag} ]]; then
	make helm-push \
		CHART_REGISTRY="${IMG_PREFIX}/charts" \
		CHART_VERSION="${CHART_VERSION}" \
		TAG="${IMG_TAG}"
else
	echo "Skipping helm-push: build was not triggered by a release tag"
fi
