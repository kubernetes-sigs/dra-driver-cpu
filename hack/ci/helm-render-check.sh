#!/bin/bash
#
# Renders the chart's DaemonSet for the kubelet roots worth checking and hands
# each one to the semantic validator in test/render. Helm and the case list live
# here so the Go side only has to judge an object it was given.

set -euo pipefail

chart=${CHART:-deployment/helm/dra-driver-cpu}
helm=${HELM:-helm}
default_root=/var/lib/kubelet

# With no cluster to ask, helm renders against its own built-in Kubernetes
# version, and in the helm the Makefile installs that version is below the
# chart's kubeVersion range, which refuses the chart outright. Named here so the
# render does not depend on which helm ran it, and named at the oldest stable
# release the range covers so these cases reach it.
kube_version=${HELM_KUBE_VERSION:-1.34.0}
template_args=(
	template test "${chart}"
	--kube-version "${kube_version}"
	--show-only templates/daemonset.yaml
)

tmp=$(mktemp -d)
trap 'rm -rf "${tmp}"' EXIT

# render <name> <root>; writes ${tmp}/<name>.yaml. It does not print where the
# file landed, because a function called in $(...) reports the status of its last
# command, so a succeeding echo would hide a failing helm and leave the test
# reading an empty file.
render() {
	local name=$1 root=$2
	local args=("${template_args[@]}")
	if [[ -n ${root} ]]; then
		args+=(--set-string "kubeletRootDir=${root}")
	fi
	"${helm}" "${args[@]}" >"${tmp}/${name}.yaml"
}

# accepts <name> <root> <expected-root> <expect-root-flag>
accepts() {
	local name=$1 root=$2 expected=$3 want_flag=$4
	echo "render check: ${name}"
	render "${name}" "${root}"
	go test -count=1 ./test/render/ -args \
		-manifest "${tmp}/${name}.yaml" \
		-expected-root "${expected}" \
		-expect-root-flag="${want_flag}"
}

# refuses <name> <message> <helm args...>; the render has to fail and name the
# guard the case is for, so an unrelated helm failure cannot satisfy it. Schema
# validation is off throughout: these cover the template's own refusals, and the
# schema is checked by helm lint and the generated-schema target.
refuses() {
	local name=$1 want=$2
	shift 2
	echo "render check: ${name} (expected to be refused)"
	if "${helm}" "${template_args[@]}" \
		"$@" >"${tmp}/${name}.yaml" 2>"${tmp}/${name}.err"; then
		echo "the chart accepted ${name}, want a refusal" >&2
		return 1
	fi
	if ! grep -Fq -- "${want}" "${tmp}/${name}.err"; then
		echo "${name} was refused for another reason, want ${want}:" >&2
		cat "${tmp}/${name}.err" >&2
		return 1
	fi
}

# Unset: the chart leaves the root to the driver, so there is no flag to check.
accepts default '' "${default_root}" false
accepts relocated /var/lib/custom-kubelet /var/lib/custom-kubelet true
# Cleaned by the template, so the mounts and the flag cannot differ by a slash.
accepts uncleaned /var/lib/custom-kubelet/ /var/lib/custom-kubelet true
# The root the chart cleans whole paths for: appending would give //plugins.
accepts root-dir / / true
# Cleaning removes the segment carrying the sequence, so nothing reaches the
# PodSpec to be reinterpreted and the value is the default after all.
accepts cleaned-away '/var/lib/$(IGNORED)/../kubelet' "${default_root}" false
# A lone $ is left alone by the kubelet, so the guard below refuses the two
# sequences rather than the character, and this is what says so.
accepts single-dollar '/var/lib/$kubelet' '/var/lib/$kubelet' true
# 73 bytes, the most the root can spend and still leave the socket at 107.
accepts socket-budget "/$(printf 'x%.0s' {1..72})" "/$(printf 'x%.0s' {1..72})" true

# The guard this change adds.
refuses dollar-paren 'must not contain' \
	--skip-schema-validation --set-string 'kubeletRootDir=/var/lib/$(KUBELET_ROOT)'
refuses double-dollar 'must not contain' \
	--skip-schema-validation --set-string 'kubeletRootDir=/var/lib/$$kubelet'

# A name ending in a space is a different directory, so trimming would move the
# mounts and the flag together to somewhere the kubelet is not.
refuses trailing-space 'must not begin or end with whitespace' \
	--skip-schema-validation --set-string 'kubeletRootDir=/var/lib/custom-kubelet '

# A key helm looks up by exact name, so a miscased one renders the default while
# the operator believes the kubelet was relocated.
refuses miscased-key 'differs from kubeletRootDir only in case' \
	--skip-schema-validation --set-string 'kubeletrootdir=/var/lib/custom-kubelet'

# One byte past what the case above spends, so the pair pins the limit rather
# than only showing that something far over it is refused.
refuses over-budget 'over the 107-byte limit' \
	--skip-schema-validation --set-string "kubeletRootDir=/$(printf 'x%.0s' {1..73})"

echo "render checks passed"
