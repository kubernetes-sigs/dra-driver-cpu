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

# platform on which we run
OS=$(shell go env GOOS)
ARCH=$(shell go env GOARCH)

# dependencies
## versions
YQ_VERSION ?= 4.47.1
# matches golang 1.25.z
GOLANGCI_LINT_VERSION ?= 2.7.2
HELM_DOCS_VERSION ?= 1.14.2
# paths
YQ = $(OUT_DIR)/yq
GOLANGCI_LINT = $(OUT_DIR)/golangci-lint

# disable CGO by default for static binaries
CGO_ENABLED=0
# remove once either we bump golang to 1.26 or https://github.com/golang/go/issues/75031 is fixed
TOOOLCHAIN_MODE ?= "$(shell go env GOVERSION)+auto"
export GOROOT GO111MODULE CGO_ENABLED

# Controls manifest generation behavior.
# Use OVERRIDE_IMAGE=true to patch the image and tag.
# Example: make manifests OVERRIDE_IMAGE=true
OVERRIDE_IMAGE ?= false

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

default: build ## Default builds

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-23s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

build: build-dracpu build-test-dracpuinfo build-test-dracputester ## build all the binaries

build-dracpu: ## build dracpu
	go build -v -o "$(OUT_DIR)/dracpu" ./cmd/dracpu

clean: ## clean
	rm -rf "$(OUT_DIR)/"

test-unit: ## run tests
	CGO_ENABLED=1 GOTOOLCHAIN=${TOOOLCHAIN_MODE} go test -v -race -count 1 -coverprofile=coverage.out ./pkg/...

update: ## runs go mod tidy and go get -u
	go get -u ./...
	go mod tidy

$(OUT_DIR):  ## creates the output directory (used internally)
	mkdir -p $(OUT_DIR)

# get image name from directory we're building
CLUSTER_NAME=dra-driver-cpu
STAGING_REPO_NAME=dra-driver-cpu
IMAGE_NAME=dra-driver-cpu
# docker image registry, default to upstream
REGISTRY ?= us-central1-docker.pkg.dev/k8s-staging-images
# this is an intentionally non-existent registry to be used only by local CI using the local image loading
REGISTRY_CI := dev.kind.local/ci
STAGING_IMAGE_NAME := ${REGISTRY}/${STAGING_REPO_NAME}/${IMAGE_NAME}
# tag based on date-sha
GIT_VERSION := $(shell date +v%Y%m%d)-$(shell git rev-parse --short HEAD)
ifneq ($(shell git status --porcelain),)
	GIT_VERSION := $(GIT_VERSION)-dirty
endif
TAG ?= $(GIT_VERSION)
# the full image tag
IMAGE_LATEST?=$(STAGING_IMAGE_NAME):latest
IMAGE := ${STAGING_IMAGE_NAME}:${TAG}
IMAGE_CI := ${REGISTRY_CI}/${IMAGE_NAME}:${TAG}
IMAGE_TEST := ${REGISTRY_CI}/${IMAGE_NAME}-test:${TAG}
# target platform(s)
PLATFORMS?=linux/amd64,linux/arm64
LOCAL_PLATFORM?=linux/$(ARCH)

# set convenient defaults for user variables
DRACPU_E2E_CPU_DEVICE_MODE ?= grouped
DRACPU_E2E_RESERVED_CPUS ?= 0
# Extra arguments passed to golangci-lint in the lint target.
# For example, set GOLANGCI_LINT_EXTRA_ARGS=--fix to auto-fix issues.
GOLANGCI_LINT_EXTRA_ARGS ?=

# shortcut
CI_MANIFEST_FILE := hack/ci/install-ci-$(DRACPU_E2E_CPU_DEVICE_MODE)-mode.yaml
# we need this because manifest processing always needs a nonempty value

# required to enable buildx
export DOCKER_CLI_EXPERIMENTAL=enabled
image: ## docker build load
	docker build . -t ${STAGING_IMAGE_NAME} --load

build-image: ## build image
	docker buildx build . \
		--platform="${LOCAL_PLATFORM}" \
		--tag="${IMAGE}" \
		--tag="${IMAGE_LATEST}" \
		--tag="${IMAGE_CI}" \
		--load

# no need to push the test image
# never push the CI image! it intentionally refers to a non-existing registry
push-image: ## build and push image directly to registry (supports multi-arch)
	-docker buildx create --name dracpu-builder
	docker buildx use dracpu-builder
	-docker buildx build . \
		--platform="${PLATFORMS}" \
		--tag="${IMAGE}" \
		--tag="${IMAGE_LATEST}" \
		--push
	-docker buildx rm dracpu-builder

kind-cluster:  ## create kind cluster
	kind create cluster --name ${CLUSTER_NAME} --config hack/kind.yaml

kind-load-image: build-image  ## load the current container image into kind
	kind load docker-image ${IMAGE} ${IMAGE_LATEST} --name ${CLUSTER_NAME}

kind-uninstall-cpu-dra: ## remove cpu dra from kind cluster
	kubectl delete -f dist/install.yaml || true

kind-install-cpu-dra: kind-uninstall-cpu-dra build-image kind-load-image ## install on cluster
	kubectl apply -f dist/install.yaml

delete-kind-cluster: ## delete kind cluster
	kind delete cluster --name ${CLUSTER_NAME}

define kind_setup
	kind create cluster --name ${CLUSTER_NAME} --config hack/ci/kind-ci.yaml
	kubectl label node ${CLUSTER_NAME}-worker node-role.kubernetes.io/worker=''
	kind load docker-image --name ${CLUSTER_NAME} ${IMAGE_CI} ${IMAGE_TEST}
	kubectl create -f $(1)
	hack/ci/wait-resourcelices.sh
endef

define generate_ci_manifest
	@cd hack/ci && cp -a ../../manifests/base/*.part.yaml .
	@# need to make kind load docker-image working as expected: see https://kind.sigs.k8s.io/docs/user/quick-start/#loading-an-image-into-your-cluster
	@$(YQ) -i '.spec.template.spec.containers[0].imagePullPolicy = "IfNotPresent"' hack/ci/daemonset-dracpu.part.yaml
	@$(YQ) -i '.spec.template.spec.containers[0].image = "${IMAGE_CI}"' hack/ci/daemonset-dracpu.part.yaml
	$(if $(2),@$(YQ) -i '$(2)' hack/ci/daemonset-dracpu.part.yaml,)
	@$(YQ) -i '.spec.template.metadata.labels["build"] = "${GIT_VERSION}"' hack/ci/daemonset-dracpu.part.yaml
	@$(YQ) '.' \
		hack/ci/clusterrole-dracpu.part.yaml \
		hack/ci/serviceaccount-dracpu.part.yaml \
		hack/ci/clusterrolebinding-dracpu.part.yaml \
		hack/ci/daemonset-dracpu.part.yaml \
		hack/ci/deviceclass-dracpu.part.yaml \
		> $(1)
	@rm hack/ci/*.part.yaml
endef

dist:
	@mkdir -p dist

manifests: dist install-yq ## create the install manifest
	@cd dist && cp -a ../manifests/base/*.part.yaml .
ifeq ($(OVERRIDE_IMAGE),true)
	@$(YQ) -i '.spec.template.spec.containers[0].image = "${IMAGE}"' dist/daemonset-dracpu.part.yaml
	@$(YQ) -i '.spec.template.spec.containers[0].imagePullPolicy = "IfNotPresent"' dist/daemonset-dracpu.part.yaml
	@$(YQ) -i '.spec.template.metadata.labels["build"] = "${GIT_VERSION}"' dist/daemonset-dracpu.part.yaml
endif
	@$(YQ) '.' \
		dist/clusterrole-dracpu.part.yaml \
		dist/serviceaccount-dracpu.part.yaml \
		dist/clusterrolebinding-dracpu.part.yaml \
		dist/daemonset-dracpu.part.yaml \
		dist/deviceclass-dracpu.part.yaml \
		> dist/install.yaml
	@rm dist/*.part.yaml

ci-manifests: install-yq ## create the CI install manifests
ifneq ($(DRACPU_E2E_VERBOSE),)
	@echo "setting up manifests for mode=$(DRACPU_E2E_CPU_DEVICE_MODE)"
endif
	$(call generate_ci_manifest,$(CI_MANIFEST_FILE),.spec.template.spec.containers[0].args |= (del(.[] | select(. == "--cpu-device-mode=*")) | . + ["--cpu-device-mode=$(DRACPU_E2E_CPU_DEVICE_MODE)", "--reserved-cpus=$(DRACPU_E2E_RESERVED_CPUS)"]))

ci-kind-setup: ci-manifests build-image build-test-image ## setup a CI cluster from scratch using reserved CPUs
ifneq ($(DRACPU_E2E_VERBOSE),)
	@echo "creating a kind cluster for mode=$(DRACPU_E2E_CPU_DEVICE_MODE)"
endif
	$(call kind_setup,$(CI_MANIFEST_FILE))

build-test-image: ## build tests image
	docker buildx build . \
		--file test/image/Dockerfile \
		--platform="${LOCAL_PLATFORM}" \
		--tag="${IMAGE_TEST}" \
		--load

build-test-dracputester: ## build helper to serve as entry point and report cpu allocation
	go build -v -o "$(OUT_DIR)/dracputester" ./test/image/dracputester

build-test-dracpuinfo: ## build helper to expose hardware info in the internal dracpu format
	go build -v -o "$(OUT_DIR)/dracpuinfo" ./test/image/dracpuinfo

test-e2e: ## run e2e test against an existing configured cluster
	env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$(DRACPU_E2E_RESERVED_CPUS) go test -v ./test/e2e/ --ginkgo.v

test-e2e-kind: ci-kind-setup test-e2e ## run e2e test against a purpose-built kind cluster

lint:  ## run the linter against the codebase
	$(GOLANGCI_LINT) run ./... $(GOLANGCI_LINT_EXTRA_ARGS)

# dependencies
.PHONY: install-yq
install-yq: $(OUT_DIR)  ## make sure the yq tool is available locally
	@# TODO: generalize platform/os?
	@if [ ! -f $(YQ) ]; then\
	       curl -L https://github.com/mikefarah/yq/releases/download/v$(YQ_VERSION)/yq_$(OS)_$(ARCH) -o $(YQ);\
               chmod 0755 $(YQ);\
	fi

.PHONY: install-golangci-lint
install-golangci-lint: $(OUT_DIR) ## make sure the golangci-lint tool is available locally
	@hack/fetch-golangci-lint.sh $(OUT_DIR) $(GOLANGCI_LINT_VERSION)

helm-lint:
	helm lint --strict deployment/helm/dra-driver-cpu

.PHONY: helm-docs
helm-docs: ## regenerate helm chart README from values.yaml annotations and README.md.gotmpl
	go run github.com/norwoodj/helm-docs/cmd/helm-docs@v$(HELM_DOCS_VERSION) --chart-search-root=deployment/helm

.PHONY: helm-docs-check
helm-docs-check: helm-docs ## verify helm chart README is up to date; fails if regeneration produces a diff
	@git diff --exit-code deployment/helm/ || \
		(echo "ERROR: Helm chart README.md is out of date. Run 'make helm-docs' to update it." && exit 1)
