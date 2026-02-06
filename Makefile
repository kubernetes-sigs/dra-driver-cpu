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

build: build-dracpu build-dracpu-admission build-test-dracpuinfo build-test-dracputester ## build all the binaries

build-dracpu: ## build dracpu
	go build -v -o "$(OUT_DIR)/dracpu" ./cmd/dracpu

build-dracpu-admission: ## build dracpu admission webhook
	go build -v -o "$(OUT_DIR)/dracpu-admission" ./cmd/dracpu-admission

clean: ## clean
	rm -rf "$(OUT_DIR)/"

test-unit: ## run tests
	CGO_ENABLED=1 go test -v -race -count 1 -coverprofile=coverage.out ./pkg/...

test-admission: ## run admission controller tests
	go test -v ./pkg/admission ./cmd/dracpu-admission

with-kind: ## run a command with a temporary kind cluster
	@if [ -z "$$CMD" ]; then \
		echo "CMD is required. Example: CMD='echo hello' $(MAKE) with-kind"; \
		exit 1; \
	fi; \
	created=0; \
	while read -r name; do \
		if [ "$$name" = "$(CLUSTER_NAME)" ]; then created=2; fi; \
	done < <(kind get clusters); \
	if [ "$$created" -eq 0 ]; then \
		kind create cluster --name ${CLUSTER_NAME} --config hack/kind.yaml; \
		created=1; \
	fi; \
	trap 'if [ "$$created" -eq 1 ]; then kind delete cluster --name ${CLUSTER_NAME}; fi' EXIT; \
	bash -c "$$CMD"

test-e2e-admission: ## run admission e2e tests (requires kind cluster)
	CMD='$(MAKE) kind-load-test-image kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; go test -v ./test/e2e/ --ginkgo.v --ginkgo.focus="Admission Webhook"' \
		$(MAKE) with-kind

test-e2e-admission-grouped-mode: ## run admission e2e tests in grouped mode
	CMD='$(MAKE) kind-load-test-image kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=grouped"]}]'"'"'; go test -v ./test/e2e/ --ginkgo.v --ginkgo.focus="Admission Webhook"' \
		$(MAKE) with-kind

test-e2e-admission-individual-mode: ## run admission e2e tests in individual mode
	CMD='$(MAKE) kind-load-test-image kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=individual"]}]'"'"'; go test -v ./test/e2e/ --ginkgo.v --ginkgo.focus="Admission Webhook"' \
		$(MAKE) with-kind

test-e2e-individual-mode: ## run e2e test reserved cpus suite
	CMD='$(MAKE) kind-load-test-image kind-install-cpu-dra kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=individual","--reserved-cpus=$(RESERVED_CPUS_E2E)"]}]'"'"'; env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$(RESERVED_CPUS_E2E) DRACPU_E2E_CPU_DEVICE_MODE=individual go test -v ./test/e2e/ --ginkgo.v' \
		$(MAKE) with-kind

test-e2e-grouped-mode: ## run e2e test grouped mode suite
	CMD='$(MAKE) kind-load-test-image kind-install-cpu-dra kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=grouped","--reserved-cpus=$(RESERVED_CPUS_E2E)"]}]'"'"'; env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$(RESERVED_CPUS_E2E) DRACPU_E2E_CPU_DEVICE_MODE=grouped go test -v ./test/e2e/ --ginkgo.v' \
		$(MAKE) with-kind

test-e2e-all: ## run all e2e tests (admission + cpu allocation)
	CMD='$(MAKE) kind-load-test-image kind-install-cpu-dra kind-install-admission; kubectl -n kube-system rollout status deploy dracpu-admission --timeout=120s; kubectl -n kube-system wait --for=condition=ready pod -l app=dracpu-admission --timeout=120s; reserved=$${DRACPU_E2E_RESERVED_CPUS:-$(RESERVED_CPUS_E2E)}; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=grouped","--reserved-cpus='"'"'$$reserved'"'"'"]}]'"'"'; env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$$reserved DRACPU_E2E_CPU_DEVICE_MODE=grouped go test -v ./test/e2e/ --ginkgo.v; kubectl -n kube-system patch daemonset dracpu --type='"'"'json'"'"' -p='"'"'[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=individual","--reserved-cpus='"'"'$$reserved'"'"'"]}]'"'"'; env DRACPU_E2E_TEST_IMAGE=$(IMAGE_TEST) DRACPU_E2E_RESERVED_CPUS=$$reserved DRACPU_E2E_CPU_DEVICE_MODE=individual go test -v ./test/e2e/ --ginkgo.v' \
		$(MAKE) with-kind

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
	@if [ "$(FORCE_BUILD)" = "1" ]; then \
		$(MAKE) build-image-force; \
	elif docker image inspect "${IMAGE}" >/dev/null 2>&1; then \
		echo "Image ${IMAGE} already exists; skipping build."; \
	else \
		$(MAKE) build-image-force; \
	fi

build-image-force: ## force build image
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

kind-load-test-image: build-test-image ## load the test image into kind
	kind load docker-image ${IMAGE_TEST} --name ${CLUSTER_NAME}

kind-uninstall-cpu-dra: ## remove cpu dra from kind cluster
	kubectl delete -f install.yaml || true

kind-uninstall-admission: ## remove admission controller from kind cluster
	kubectl delete -f install-admission.yaml || true
	kubectl -n kube-system delete secret dracpu-admission-tls || true

kind-install-all: kind-install-cpu-dra kind-install-admission ## by default, is grouped mode. Use kind-install-indiviudal-mode to install individual mode

kind-install-individual-mode: kind-install-all ## install individual mode on kind
	kubectl -n kube-system patch daemonset dracpu --type='json' -p='[{"op":"replace","path":"/spec/template/spec/containers/0/args","value":["/dracpu","--v=4","--cpu-device-mode=individual"]}]'

kind-install-cpu-dra: kind-uninstall-cpu-dra build-image kind-load-image ## install on cluster
	kubectl apply -f install.yaml

kind-install-admission: kind-uninstall-admission build-image kind-load-image ## install admission controller only
	kubectl apply -f install-admission.yaml
	kubectl -n kube-system patch deploy dracpu-admission --type='json' -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'
	bash hack/webhook/generate-certs.sh
	kubectl -n kube-system rollout restart deploy dracpu-admission

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

RESERVED_CPUS_E2E ?= 0
ci-manifests-individual-mode: install.yaml install-yq ## create the CI install manifests for the individual mode test variant
	$(call generate_ci_manifest,hack/ci/install-ci-individual-mode.yaml,.spec.template.spec.containers[0].args |= (del(.[] | select(. == "--cpu-device-mode=*")) | . + ["--cpu-device-mode=individual", "--reserved-cpus=$(RESERVED_CPUS_E2E)"]))

ci-kind-setup-individual-mode: ci-manifests-individual-mode build-image build-test-image ## setup a CI cluster from scratch for the reserved cpus test variant
	$(call kind_setup,hack/ci/install-ci-individual-mode.yaml)

ci-manifests-grouped-mode: install.yaml install-yq ## create the CI install manifests for the grouped mode test variant
	$(call generate_ci_manifest,hack/ci/install-ci-grouped-mode.yaml,.spec.template.spec.containers[0].args |= (del(.[] | select(. == "--cpu-device-mode=*")) | . + ["--cpu-device-mode=grouped", "--reserved-cpus=$(RESERVED_CPUS_E2E)"]))

ci-kind-setup-grouped-mode: ci-manifests-grouped-mode build-image build-test-image ## setup a CI cluster from scratch for the reserved cpus test variant
	$(call kind_setup,hack/ci/install-ci-grouped-mode.yaml)

build-test-image: ## build tests image
	@if [ "$(FORCE_BUILD)" = "1" ]; then \
		$(MAKE) build-test-image-force; \
	elif docker image inspect "${IMAGE_TEST}" >/dev/null 2>&1; then \
		echo "Image ${IMAGE_TEST} already exists; skipping build."; \
	else \
		$(MAKE) build-test-image-force; \
	fi

build-test-image-force: ## force build tests image
	docker buildx build . \
		--file test/image/Dockerfile \
		--platform="${PLATFORMS}" \
		--tag="${IMAGE_TEST}" \
		--load

build-test-dracputester: ## build helper to serve as entry point and report cpu allocation
	go build -v -o "$(OUT_DIR)/dracputester" ./test/image/dracputester

build-test-dracpuinfo: ## build helper to expose hardware info in the internal dracpu format
	go build -v -o "$(OUT_DIR)/dracpuinfo" ./test/image/dracpuinfo

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
	@hack/fetch-golangci-lint.sh $(OUT_DIR) $(GOLANGCI_LINT_VERSION)
