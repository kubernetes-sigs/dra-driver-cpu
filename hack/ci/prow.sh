#!/bin/bash
set -euxo pipefail
# 1. Pin the version for CI stability
KIND_VERSION="v0.31.0"
# 2. setup host
mkdir $HOME/bin
export PATH="$HOME/bin:$PATH"
# 3. Download the binary directly
curl -Lo $HOME/bin/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
# 4. Make it executable
chmod +x $HOME/bin/kind
# 5. Prepare the kind cluster
make ci-kind-setup
# 6. Run the E2E tests - note this is the default KUBECONFIG setup kind does.
KUBECONFIG=${HOME}/.kube/config make test-e2e
