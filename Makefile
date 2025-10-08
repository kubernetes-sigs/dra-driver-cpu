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
# matches golang 1.24.z
GOLANGCI_LINT_VERSION ?= 2.3.0
# paths
YQ = $(OUT_DIR)/yq
GOLANGCI_LINT = $(OUT_DIR)/golangci-lint

# disable CGO by default for static binaries
CGO_ENABLED=0
export GOROOT GO111MODULE CGO_ENABLED

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
	CGO_ENABLED=1 go test -v -race -count 1 -coverprofile=coverage.out ./pkg/...

update: ## runs go mod tidy
	go mod tidy

$(OUT_DIR):  ## creates the output directory (used internally)
	mkdir -p $(OUT_DIR)

# get image name from directory we're building
CLUSTER_NAME=dra-driver-cpu
STAGING_REPO_NAME=dra-driver-cpu
IMAGE_NAME=dra-driver-cpu
# docker image registry, default to upstream
REGISTRY := us-central1-docker.pkg.dev/k8s-staging-images
# this is an intentionally non-existent registry to be used only by local CI using the local image loading
REGISTRY_CI := dev.kind.local/ci
STAGING_IMAGE_NAME := ${REGISTRY}/${STAGING_REPO_NAME}/${IMAGE_NAME}
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
IMAGE_CI := ${REGISTRY_CI}/${IMAGE_NAME}:${TAG}
IMAGE_TEST := ${REGISTRY_CI}/${IMAGE_NAME}-test:${TAG}
# target platform(s)
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
		--tag="${IMAGE_CI}" \
		--load

# no need to push the test image
# never push the CI image! it intentionally refers to a non-existing registry
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

define kind_setup
	kind create cluster --name ${CLUSTER_NAME} --config hack/ci/kind-ci.yaml
	kubectl label node ${CLUSTER_NAME}-worker node-role.kubernetes.io/worker=''
	kind load docker-image --name ${CLUSTER_NAME} ${IMAGE_CI} ${IMAGE_TEST}
	kubectl create -f $(1)
	hack/ci/wait-resourcelices.sh
endef

define generate_ci_manifest
	@cd hack/ci && $(YQ) e -s '(.kind | downcase) + "-" + .metadata.name + ".part.yaml"' ../../install.yaml
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
		hack/ci/deviceclass-dra.cpu.part.yaml \
		> $(1)
	@rm hack/ci/*.part.yaml
endef

ci-manifests: install.yaml install-yq ## create the CI install manifests
	$(call generate_ci_manifest,hack/ci/install-ci.yaml)

ci-kind-setup: ci-manifests build-image build-test-image ## setup a CI cluster from scratch
	$(call kind_setup,hack/ci/install-ci.yaml)

RESERVED_CPUS_E2E ?= 0,1
ci-manifests-reserved-cpus: install.yaml install-yq ## create the CI install manifests for the reserved cpus test variant
	$(call generate_ci_manifest,hack/ci/install-ci-reserved-cpus.yaml,.spec.template.spec.containers[0].args += "--reserved-cpus=$(RESERVED_CPUS_E2E)")

ci-kind-setup-reserved-cpus: ci-manifests-reserved-cpus build-image build-test-image ## setup a CI cluster from scratch for the reserved cpus test variant
	$(call kind_setup,hack/ci/install-ci-reserved-cpus.yaml)

build-test-image: ## build tests image
	docker buildx build . \
		--file test/image/Dockerfile \
		--platform="${PLATFORMS}" \
		--tag="${IMAGE_TEST}" \
		--load

build-test-dracputester: ## build helper to serve as entry point and report cpu allocation
	go build -v -o "$(OUT_DIR)/dracputester" ./test/image/dracputester

build-test-dracpuinfo: ## build helper to expose hardware info in the internal dracpu format
	go build -v -o "$(OUT_DIR)/dracpuinfo" ./test/image/dracpuinfo

test-e2e-base: ## run e2e test base suite
	env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) go test -v ./test/e2e/ --ginkgo.v

test-e2e-reserved-cpus: ## run e2e test reserved cpus suite
	env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$(RESERVED_CPUS_E2E) go test -v ./test/e2e/ --ginkgo.v

lint:  ## run the linter against the codebase
	$(GOLANGCI_LINT) run ./...

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
	@[ ! -f $(OUT_DIR)/golangci-lint ] && { \
		command -v golangci-lint >/dev/null 2>&1 && {\
			ln -sf $(shell command -v golangci-lint ) $(OUT_DIR) ;\
			echo "reusing system golangci-lint" ;\
		} || { \
			curl -sSL "https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_LINT_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)-$(OS)-$(ARCH).tar.gz" -o $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION)-$(OS)-$(ARCH).tar.gz ;\
			tar -x -C $(OUT_DIR) -f $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION)-$(OS)-$(ARCH).tar.gz ;\
			ln -sf $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION)-$(OS)-$(ARCH)/golangci-lint $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION) ;\
			ln -sf $(GOLANGCI_LINT)-$(GOLANGCI_LINT_VERSION) $(GOLANGCI_LINT) ;\
		}; \
	} || true
