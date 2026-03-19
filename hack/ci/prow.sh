#!/bin/bash
set -euxo pipefail

make ci-kind-setup
# Note this is the default KUBECONFIG setup kind does.
KUBECONFIG=${HOME}/.kube/config make test-e2e
