# Copyright 2025 Kubernetes Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

REPO_ROOT:=${CURDIR}
OUT_DIR=$(REPO_ROOT)/bin

# disable CGO by default for static binaries
CGO_ENABLED=0
export GOROOT GO111MODULE CGO_ENABLED

# Setting SHELL to bash allows bash commands to be executed by recipes.                                                                            # Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

default: build ## Default builds

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)


build: build-dracpu ## build dracpu

build-dracpu: ## build dracpu
	go build -v -o "$(OUT_DIR)/dracpu" ./cmd/dracpu


clean: ## clean
	rm -rf "$(OUT_DIR)/"

test-unit: ## run tests
	CGO_ENABLED=1 go test -v -race -count 1 ./pkg/...

update: ## runs go mod tidy
	go mod tidy

# get image name from directory we're building
CLUSTER_NAME=dra-driver-cpu
IMAGE_NAME=dra-driver-cpu
# docker image registry, default to upstream
REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images
STAGING_IMAGE_NAME := ${REGISTRY}/${IMAGE_NAME}
TESTING_IMAGE_NAME := ${REGISTRY}/${IMAGE_NAME}-test
# tag based on date-sha
GIT_VERSION := $(shell date +v%Y%m%d)-$(shell git rev-parse --short HEAD)
ifneq ($(shell git status --porcelain),)
	GIT_VERSION := $(GIT_VERSION)-dirty
endif
TAG ?= $(GIT_VERSION)
# the full image tag
IMAGE_LATEST?=$(STAGING_IMAGE_NAME):latest
IMAGE := ${STAGING_IMAGE_NAME}:${TAG}
IMAGE_TESTING := "${TESTING_IMAGE_NAME}:${TAG}"
PLATFORMS?=linux/amd64

# required to enable buildx
export DOCKER_CLI_EXPERIMENTAL=enabled
image: ## docker build load
	docker build . -t ${STAGING_IMAGE_NAME} --load

build-image: ## build image
	docker buildx build . \
		--platform="${PLATFORMS}" \
		--tag="${IMAGE}" \
		--tag="${IMAGE_LATEST}" \
		--load

push-image: build-image ## build and push image
	docker push ${IMAGE}
	docker push ${IMAGE_LATEST}

kind-cluster:  ## create kind cluster
	kind create cluster --name ${CLUSTER_NAME} --config hack/kind.yaml

kind-load-image: build-image  ## load the current container image into kind
	kind load docker-image ${IMAGE} ${IMAGE_LATEST} --name ${CLUSTER_NAME}

kind-uninstall-cpu-dra: ## remove cpu dra from kind cluster
	kubectl delete -f install.yaml || true

kind-install-cpu-dra: kind-uninstall-cpu-dra build-image kind-load-image ## install on cluster
	kubectl apply -f install.yaml

delete-kind-cluster: ## delete kind cluster
	kind delete cluster --name ${CLUSTER_NAME}

ci-kind-install: kind-uninstall-cpu-dra build-image kind-load-image ## install on CI cluster
	kubectl create -f hack/ci/install-ci.yaml
	kubectl label node ${CLUSTER_NAME}-worker node-role.kubernetes.io/worker=''
	hack/ci/wait-resourcelices.sh

ci-kind-load-image: build-test-image  ## load the testing container image into kind
	kind load docker-image ${IMAGE_TESTING} --name ${CLUSTER_NAME}

ci-manifests: install.yaml install-yq ## create the CI install manifests
	@cd hack/ci && ../../bin/yq e -s '"_" + (.kind | downcase) + "-" + .metadata.name + ".yaml"' ../../install.yaml
	@# need to make kind load docker-image working as expected: see https://kind.sigs.k8s.io/docs/user/quick-start/#loading-an-image-into-your-cluster
	@bin/yq -i '.spec.template.spec.containers[0].imagePullPolicy = "IfNotPresent"' hack/ci/_daemonset-dracpu.yaml
	@bin/yq -i '.spec.template.spec.containers[0].image = "${IMAGE}"' hack/ci/_daemonset-dracpu.yaml
	@bin/yq -i '.spec.template.metadata.labels["build"] = "${GIT_VERSION}"' hack/ci/_daemonset-dracpu.yaml
	@bin/yq '.' \
		hack/ci/_clusterrole-dracpu.yaml \
		hack/ci/_serviceaccount-dracpu.yaml \
		hack/ci/_clusterrolebinding-dracpu.yaml \
		hack/ci/_daemonset-dracpu.yaml \
		hack/ci/_deviceclass-dra.cpu.yaml \
		> hack/ci/install-ci.yaml

build-test-image: ## build tests image
	docker buildx build . \
		--file test/image/Dockerfile \
		--platform="${PLATFORMS}" \
		--tag="${REGISTRY}/${IMAGE_NAME}-test:${TAG}" \
		--load

test-dracputester: ## build helper to serve as entry point and report cpu allocation
	go build -v -o "$(OUT_DIR)/dracputester" ./test/image/dracputester

test-dracpuinfo: ## build helper to expose hardware info in the internal dracpu format
	go build -v -o "$(OUT_DIR)/dracpuinfo" ./test/image/dracpuinfo

test-e2e-base: ## run e2e test base suite
	env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TESTING) go test -v ./test/e2e/ --ginkgo.v

# dependencies
.PHONY:
install-yq: ## make sure the yq tool is available locally
	@# TODO: generalize platform/os?
	@if [ ! -f bin/yq ]; then\
	       mkdir -p bin;\
	       curl -L https://github.com/mikefarah/yq/releases/download/v4.47.1/yq_linux_amd64 -o bin/yq;\
               chmod 0755 bin/yq;\
	fi
