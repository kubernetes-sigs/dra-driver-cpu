#!/bin/bash
# Regression checks for the rendered DaemonSet that a schema/lint pass cannot
# catch: the kubelet root has to produce the mount paths and the driver flag
# from one value, or the plugin registers on a path the kubelet never watches.
# No cluster needed.
set -euo pipefail

CHART="deployment/helm/dra-driver-cpu"
fail=0

ok() { printf 'PASS: %s\n' "$1"; }
bad() {
	printf 'FAIL: %s\n' "$1"
	fail=1
}

# 1. A custom kubelet root derives the matching plugins / plugins_registry
#    paths and is passed to the driver, and a non-canonical path is cleaned the
#    same way filepath.Clean cleans it on the driver side.
for pair in \
	"/mnt/fast/k8s/kubelet|/mnt/fast/k8s/kubelet" \
	"/mnt/a/../kubelet//|/mnt/kubelet" \
	"/|"; do
	root="${pair%%|*}"
	want="${pair##*|}"
	out=$(helm template t "$CHART" --set "kubeletRootDir=$root" 2>/dev/null)
	if printf '%s\n' "$out" | grep -q "$want/plugins$" &&
		printf '%s\n' "$out" | grep -q "$want/plugins_registry$"; then
		ok "root '$root' derives '$want/plugins{,_registry}'"
	else
		bad "root '$root' did not derive '$want/plugins{,_registry}'"
	fi
	# The mounts and the flag must agree: the kubelet looks for the registrar
	# socket under its own root, so a driver told a different root registers
	# somewhere the kubelet never watches.
	if printf '%s\n' "$out" | grep -q -- "--kubelet-root-dir=${want:-/}$"; then
		ok "root '$root' reaches the driver as --kubelet-root-dir=${want:-/}"
	else
		bad "root '$root' did not reach the driver as --kubelet-root-dir=${want:-/}"
	fi
done

# 2. A relative kubelet root is rejected (schema pattern and template guard).
if helm template t "$CHART" --set kubeletRootDir=relative/x >/dev/null 2>&1; then
	bad "relative kubeletRootDir was accepted"
else
	ok "relative kubeletRootDir rejected"
fi

# 3. Default render is unchanged: the standard /var/lib/kubelet paths.
def=$(helm template t "$CHART" 2>/dev/null)
if printf '%s\n' "$def" | grep -q '/var/lib/kubelet/plugins$' &&
	printf '%s\n' "$def" | grep -q -- '--kubelet-root-dir=/var/lib/kubelet$'; then
	ok "default render keeps the /var/lib/kubelet paths"
else
	bad "default render drifted"
fi

exit "$fail"
