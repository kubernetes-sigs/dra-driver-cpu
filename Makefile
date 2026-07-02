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
# matches golang 1.26.z
GOLANGCI_LINT_VERSION ?= 2.12.2
HELM_DOCS_VERSION ?= 1.14.2
HELM_SCHEMA_VERSION ?= 2.3.1
KIND_K8S_VERSION ?= v1.36.0
GOPLS_VERSION ?= v0.22.0
# paths
YQ = $(OUT_DIR)/yq
GOLANGCI_LINT = $(OUT_DIR)/golangci-lint
HELM := $(or $(shell command -v helm 2>/dev/null),$(OUT_DIR)/helm)

# disable CGO by default for static binaries
CGO_ENABLED=0
# remove once either we bump golang to 1.26 or https://github.com/golang/go/issues/75031 is fixed
TOOOLCHAIN_MODE ?= "$(shell go env GOVERSION)+auto"
KUBECONFIG ?= $(HOME)/.kube/config
export GOROOT GO111MODULE CGO_ENABLED KUBECONFIG

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

build: build-dracpu build-dracpu-gatherinfo build-test-dracpuinfo build-test-dracputester ## build all the binaries

build-dracpu: ## build dracpu
	go build -v -o "$(OUT_DIR)/dracpu" ./cmd/dracpu

build-dracpu-gatherinfo: ## build dracpu-gatherinfo
	go build -v -o "$(OUT_DIR)/dracpu-gatherinfo" ./cmd/dracpu

clean: ## clean
	rm -rf "$(OUT_DIR)/"

test-unit: ## run tests
	CGO_ENABLED=1 GOTOOLCHAIN=${TOOOLCHAIN_MODE} go test -v -race -count 1 -coverprofile=coverage.out ./pkg/...

test-e2e-local: ## run e2e local tests (binary must be pre-built)
	go test -v -count 1 ./test/e2e_local/...

update: ## runs go mod tidy and go get -u
	go get -u ./...
	go mod tidy

$(OUT_DIR):  ## creates the output directory (used internally)
	mkdir -p $(OUT_DIR)

# get image name from directory we're building
CLUSTER_NAME=dra-driver-cpu
IMAGE_NAME=dra-driver-cpu
# docker image registry, default to upstream
REGISTRY ?= registry.k8s.io/k8s-staging-images
# this is an intentionally non-existent registry to be used only by local CI using the local image loading
REGISTRY_CI := dev.kind.local/ci
IMAGE_REPO := ${REGISTRY}/${IMAGE_NAME}/${IMAGE_NAME}
IMAGE_REPO_CI := ${REGISTRY_CI}/${IMAGE_NAME}
# tag based on date-sha
GIT_VERSION := $(shell date +v%Y%m%d)-$(shell git rev-parse --short HEAD)
ifneq ($(shell git status --porcelain),)
	GIT_VERSION := $(GIT_VERSION)-dirty
endif
TAG ?= $(GIT_VERSION)
# the full image tag
IMAGE_LATEST?=$(IMAGE_REPO):latest
IMAGE := ${IMAGE_REPO}:${TAG}
IMAGE_CI := ${IMAGE_REPO_CI}:${TAG}
IMAGE_TEST := ${IMAGE_REPO_CI}-test:${TAG}
# target platform(s)
PLATFORMS?=linux/amd64,linux/arm64
LOCAL_PLATFORM?=linux/$(ARCH)

# set convenient defaults for user variables
DRACPU_E2E_CPU_DEVICE_MODE ?= grouped
DRACPU_E2E_CPU_GROUP_BY ?= numanode
DRACPU_E2E_RESERVED_CPUS ?= 0
# Extra arguments passed to golangci-lint in the lint target.
# For example, set GOLANGCI_LINT_EXTRA_ARGS=--fix to auto-fix issues.
GOLANGCI_LINT_EXTRA_ARGS ?=

HELM_CHART := deployment/helm/dra-driver-cpu
HELM_COMMON_ARGS := \
	--namespace kube-system \
	--set fullnameOverride=dracpu \
	--set podLabels.app=dracpu

# required to enable buildx
export DOCKER_CLI_EXPERIMENTAL=enabled
image: ## docker build load
	docker build . -t ${IMAGE_REPO} --load

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

# Builds the Kind node image on-the-fly if it is not pre-cached locally.
ensure-kind-node-image:
	@if [ -z "$$(docker images -q kindest/node:$(KIND_K8S_VERSION) 2>/dev/null)" ]; then \
		echo "Building kindest/node:$(KIND_K8S_VERSION) ..."; \
		kind build node-image --image=kindest/node:$(KIND_K8S_VERSION) $(KIND_K8S_VERSION); \
	fi

kind-cluster: ensure-kind-node-image ## create kind cluster
	kind create cluster --name ${CLUSTER_NAME} --image=kindest/node:$(KIND_K8S_VERSION) --config hack/kind.yaml

kind-load-image: build-image  ## load the current container image into kind
	kind load docker-image ${IMAGE} ${IMAGE_LATEST} --name ${CLUSTER_NAME}

kind-uninstall-cpu-dra: ## remove cpu dra from kind cluster
	$(HELM) uninstall dra-driver-cpu --namespace kube-system || true

kind-install-cpu-dra: kind-uninstall-cpu-dra build-image kind-load-image ## install on cluster mimicking production settings
	$(HELM) install dra-driver-cpu ${HELM_CHART} ${HELM_COMMON_ARGS} \
		--set image.repository=${IMAGE_REPO} \
		--set image.tag=${TAG} \
		--set image.pullPolicy=IfNotPresent

delete-kind-cluster: ## delete kind cluster
	kind delete cluster --name ${CLUSTER_NAME}

dist:
	@mkdir -p dist

manifests: dist install-helm ## create rendered helm manifest
ifeq ($(OVERRIDE_IMAGE),true)
	$(HELM) template dra-driver-cpu ${HELM_CHART} ${HELM_COMMON_ARGS} \
		--set podLabels.build=${GIT_VERSION} \
		--set image.repository=${IMAGE_REPO} \
		--set image.tag=${TAG} \
		--set image.pullPolicy=IfNotPresent \
		> dist/helm-manifest.yaml
else
	$(HELM) template dra-driver-cpu ${HELM_CHART} ${HELM_COMMON_ARGS} \
		> dist/helm-manifest.yaml
endif

ci-kind-setup: ensure-kind-node-image install-helm build-image build-test-image ## setup a CI cluster from scratch using reserved CPUs
ifneq ($(DRACPU_E2E_VERBOSE),)
	@echo "creating a kind cluster for mode=$(DRACPU_E2E_CPU_DEVICE_MODE)"
endif
	kind create cluster --name ${CLUSTER_NAME} --image=kindest/node:$(KIND_K8S_VERSION) --config hack/ci/kind-ci.yaml
	kubectl label node ${CLUSTER_NAME}-worker node-role.kubernetes.io/worker=''
	kind load docker-image --name ${CLUSTER_NAME} ${IMAGE_CI} ${IMAGE_TEST}
	$(HELM) install dra-driver-cpu ${HELM_CHART} ${HELM_COMMON_ARGS} \
		--set image.repository=${IMAGE_REPO_CI} \
		--set image.tag=${TAG} \
		--set image.pullPolicy=IfNotPresent \
		--set args.logLevel=6 \
		--set args.cpuDeviceMode=${DRACPU_E2E_CPU_DEVICE_MODE} \
		--set args.groupBy=${DRACPU_E2E_CPU_GROUP_BY} \
		--set-string args.reservedCPUs=${DRACPU_E2E_RESERVED_CPUS} \
		--set args.exposePCIeRoots=true
	hack/ci/wait-resourcelices.sh

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

modernize: ## run modernize to report suggested code modernizations
	go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@$(GOPLS_VERSION) -diff ./...

modernize-fix: ## apply modernize suggestions
	go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@$(GOPLS_VERSION) -fix ./...

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

helm-lint: install-helm ## lint helm chart with strict mode
	$(HELM) lint --strict ${HELM_CHART}

.PHONY: helm-docs
helm-docs: ## regenerate helm chart README from values.yaml annotations and README.md.gotmpl
	go run github.com/norwoodj/helm-docs/cmd/helm-docs@v$(HELM_DOCS_VERSION) --chart-search-root=deployment/helm

.PHONY: helm-docs-check
helm-docs-check: helm-docs ## verify helm chart README is up to date; fails if regeneration produces a diff
	@git diff --exit-code deployment/helm/ || \
		(echo "ERROR: Helm chart README.md is out of date. Run 'make helm-docs' to update it." && exit 1)

.PHONY: helm-schema
helm-schema: ## regenerate values.schema.json from values.yaml @schema annotations
	go run github.com/losisin/helm-values-schema-json/v2@v$(HELM_SCHEMA_VERSION) \
		-f ${HELM_CHART}/values.yaml \
		-o ${HELM_CHART}/values.schema.json \
		--use-helm-docs \
		--bundle \
		--bundle-root ${HELM_CHART} \
		--indent 2

.PHONY: helm-schema-check
helm-schema-check: helm-schema ## verify values.schema.json is up to date; fails if regeneration produces a diff
	@git diff --exit-code ${HELM_CHART}/values.schema.json || \
		(echo "ERROR: values.schema.json is out of date. Run 'make helm-schema' to update it." && exit 1)
CHART_REGISTRY?=$(REGISTRY)/$(IMAGE_NAME)/charts
HELM_VERSION_SHA?=a2369ca71c0ef633bf6e4fccd66d634eb379b371 # v3.20.1

.PHONY: install-helm
install-helm: $(OUT_DIR)
	@if ! $(HELM) version >/dev/null 2>&1; then \
		echo "Helm not found, installing helm@$(HELM_VERSION_SHA) ..."; \
		GOBIN=$(OUT_DIR) go install helm.sh/helm/v3/cmd/helm@$(HELM_VERSION_SHA); \
	fi

.PHONY: helm-package
helm-package: install-helm ## package helm chart for release
	$(HELM) package ${HELM_CHART} \
		--version "$(CHART_VERSION)" \
		--app-version "$(TAG)" \
		--destination $(OUT_DIR)

.PHONY: helm-push
helm-push: helm-package ## push helm chart to OCI registry
	$(HELM) push $(OUT_DIR)/dra-driver-cpu-$(CHART_VERSION).tgz oci://$(CHART_REGISTRY)
