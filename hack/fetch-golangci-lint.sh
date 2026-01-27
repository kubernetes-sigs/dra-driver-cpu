#!/bin/sh

OUT_DIR="${1}"
GOLANGCI_LINT_VERSION="${2:-2.7.0}"

if [ -z "${OUT_DIR}" ]; then
	echo "need output dir as argument" 1>&2
	exit 1
fi

# export needed to make the trap work
export GOLANGCI_LINT="${OUT_DIR}/golangci-lint"
# platform on which we run
OS=$(go env GOOS)
ARCH=$(go env GOARCH)

trap '${GOLANGCI_LINT} version' EXIT

if [ -x "${GOLANGCI_LINT}" ]; then
	echo "golangci-lint already set"
	exit 0
fi

if command -v golangci-lint >/dev/null 2>&1; then
	echo "reusing system golangci-lint"
	ln -sf "$(command -v golangci-lint)" "${OUT_DIR}"
	exit 0
fi

echo "fetching golangci-lint"
curl -sSL "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/golangci-lint-${GOLANGCI_LINT_VERSION}-${OS}-${ARCH}.tar.gz" -o "${GOLANGCI_LINT}-${GOLANGCI_LINT_VERSION}-${OS}-${ARCH}.tar.gz"
tar -x -C "${OUT_DIR}" -f "${GOLANGCI_LINT}-${GOLANGCI_LINT_VERSION}-${OS}-${ARCH}.tar.gz"
ln -sf "${GOLANGCI_LINT}-${GOLANGCI_LINT_VERSION}-${OS}-${ARCH}/golangci-lint" "${GOLANGCI_LINT}-${GOLANGCI_LINT_VERSION}"
ln -sf "${GOLANGCI_LINT}-${GOLANGCI_LINT_VERSION}" "${GOLANGCI_LINT}"
