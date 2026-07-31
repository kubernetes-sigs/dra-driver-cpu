#!/bin/bash
# Regression checks for the rendered DaemonSet that lint and schema validation
# cannot catch: the kubelet root has to produce both mount paths and the driver
# flag from one value, or the driver registers on a path the kubelet never
# watches. That is the failure this value exists to prevent, and every guard
# below is one of the ways it came back. No cluster needed.
set -euo pipefail

CHART="deployment/helm/dra-driver-cpu"
fail=0

ok() { printf 'PASS: %s\n' "$1"; }
bad() {
	printf 'FAIL: %s\n' "$1"
	fail=1
}

# Fixed-string, because roots under test contain regex metacharacters. A here
# string rather than a pipe: grep -q exits on the first match, and under pipefail
# the SIGPIPE that gives the writer would report a found needle as missing.
has() { grep -qF -- "$1" <<<"$2"; }

# Kept out of the rendered document. Merging the two streams would put any
# warning Helm writes into the YAML that yq is handed below, and a parse failure
# there exits the script under set -e with nothing said about why.
errors=$(mktemp)
trap 'rm -f "$errors"' EXIT

# Renders into `out`, or reports why it could not and answers false. Reported as
# its own failure rather than left to the assertions, which would otherwise
# blame the paths for a render that never happened.
render() { # label, helm arguments
	local label=$1
	shift
	if out=$(helm template t "$CHART" "$@" 2>"$errors"); then
		return 0
	fi
	bad "$label failed to render: $(head -n 1 "$errors")"
	return 1
}

# Each named mount and volume has to point at its own directory: swapping
# device-plugin and plugin-registry keeps both strings in the render while moving
# the socket the kubelet watches. The container is chosen by name so a sidecar
# cannot move what is read. Reads the document `render` left in `out`.
check_bindings() { # label, plugins dir, registry dir
	local label=$1 plugins=$2 registry=$3 pairs want
	if ! pairs=$(printf '%s' "$out" | yq eval-all --no-doc '
		select(.kind == "DaemonSet") |
		(.spec.template.spec.containers[] | select(.name == "dracpu") | .volumeMounts[] | select(.name == "device-plugin") | "mount/device-plugin=" + .mountPath),
		(.spec.template.spec.containers[] | select(.name == "dracpu") | .volumeMounts[] | select(.name == "plugin-registry") | "mount/plugin-registry=" + .mountPath),
		(.spec.template.spec.volumes[] | select(.name == "device-plugin") | "host/device-plugin=" + .hostPath.path),
		(.spec.template.spec.volumes[] | select(.name == "plugin-registry") | "host/plugin-registry=" + .hostPath.path)
	' - 2>"$errors" | sort); then
		bad "$label rendered a document yq could not read: $(head -n 1 "$errors")"
		return
	fi
	want=$(printf 'host/device-plugin=%s\nhost/plugin-registry=%s\nmount/device-plugin=%s\nmount/plugin-registry=%s\n' \
		"$plugins" "$registry" "$plugins" "$registry" | sort)
	if [ "$pairs" = "$want" ]; then
		ok "$label binds each named mount and volume to its own directory"
	else
		bad "$label named bindings are $(printf '%s' "$pairs" | tr '\n' ' '), wanted $(printf '%s' "$want" | tr '\n' ' ')"
	fi
}

# The whole rendered path is asserted rather than a root plus a suffix: with
# root "/" the two differ, and that is exactly the case worth pinning.
while IFS='|' read -r root plugins registry flag; do
	label="root '$root'"
	render "$label" --set-string "kubeletRootDir=$root" || continue
	if has "mountPath: \"$plugins\"" "$out" && has "path: \"$plugins\"" "$out" &&
		has "mountPath: \"$registry\"" "$out" && has "path: \"$registry\"" "$out"; then
		ok "$label mounts $plugins and $registry"
	else
		bad "$label did not mount $plugins and $registry"
	fi
	# Both default paths have to be gone, not merely joined by the new ones. The
	# dangerous shape is a half migration that moves the plugin-data mount and leaves
	# the registrar under /var/lib/kubelet/plugins_registry.
	#
	# Matched with its quotes rather than as a bare substring, since a root of
	# /var/lib/kubelet/plugins-alt renders paths containing the default one, and a
	# bare /var/lib/kubelet/plugins also matches plugins_registry. Skipped only for a
	# root that renders that exact path; a substring skip dropped the assertion for
	# the very roots the quotes were added for.
	left_behind=""
	for stale_path in "/var/lib/kubelet/plugins" "/var/lib/kubelet/plugins_registry"; do
		case "$stale_path" in "$plugins" | "$registry") continue ;; esac
		if has ": \"$stale_path\"" "$out"; then
			left_behind="$left_behind $stale_path"
		fi
	done
	if [ -n "$left_behind" ]; then
		bad "$label left the default path(s)$left_behind behind"
	else
		ok "$label leaves no default kubelet path behind"
	fi
	check_bindings "$label" "$plugins" "$registry"
	# The kubelet looks for the registrar socket under its own root, so a driver
	# told a different root registers where the kubelet is not watching.
	if has "\"--kubelet-root-dir=$flag\"" "$out"; then
		ok "$label reaches the driver as --kubelet-root-dir=$flag"
	else
		bad "$label did not reach the driver as --kubelet-root-dir=$flag"
	fi
done <<-'EOF'
	/mnt/fast/k8s/kubelet|/mnt/fast/k8s/kubelet/plugins|/mnt/fast/k8s/kubelet/plugins_registry|/mnt/fast/k8s/kubelet
	/mnt/a/../kubelet//|/mnt/kubelet/plugins|/mnt/kubelet/plugins_registry|/mnt/kubelet
	/|/plugins|/plugins_registry|/
	/mnt/with #tag/kubelet|/mnt/with #tag/kubelet/plugins|/mnt/with #tag/kubelet/plugins_registry|/mnt/with #tag/kubelet
	/mnt/with: colon/kubelet|/mnt/with: colon/kubelet/plugins|/mnt/with: colon/kubelet/plugins_registry|/mnt/with: colon/kubelet
	/mnt/kubelet/x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../x/../|/mnt/kubelet/plugins|/mnt/kubelet/plugins_registry|/mnt/kubelet
	/var/lib/kubelet/plugins-alt|/var/lib/kubelet/plugins-alt/plugins|/var/lib/kubelet/plugins-alt/plugins_registry|/var/lib/kubelet/plugins-alt
	/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx|/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx/plugins|/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx/plugins_registry|/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx
EOF

# The default keeps the standard paths and emits no flag: it is newer than the
# chart's appVersion, so the default image would exit on an unknown flag. Checked
# through the same four named bindings as every other root, because two string
# assertions covered one mount and the other volume and missed a swap.
if render "default render"; then
	if has 'mountPath: "/var/lib/kubelet/plugins"' "$out" &&
		has 'path: "/var/lib/kubelet/plugins_registry"' "$out"; then
		ok "default render keeps the /var/lib/kubelet paths"
	else
		bad "default render drifted from /var/lib/kubelet"
	fi
	check_bindings "default render" /var/lib/kubelet/plugins /var/lib/kubelet/plugins_registry
	if has 'kubelet-root-dir' "$out"; then
		bad "default render passes --kubelet-root-dir to an image that predates it"
	else
		ok "default render passes no --kubelet-root-dir"
	fi
fi

# A root the chart cannot honour is refused whether or not the values schema
# runs. The schema rejects most of these before the helper is reached, so testing
# only the default run would leave the template's own guards unproven under
# --skip-schema-validation. Which layer refuses a root is not asserted.
#
# <SP> stands in for a literal space, which trailing-whitespace tooling would
# otherwise strip out of this file.
while IFS='|' read -r label root; do
	root=${root//<SP>/ }
	for mode in "with the values schema" "with the values schema skipped"; do
		if [ "$mode" = "with the values schema skipped" ]; then
			set -- --skip-schema-validation
		else
			set --
		fi
		if helm template t "$CHART" "$@" --set-string "kubeletRootDir=$root" >/dev/null 2>&1; then
			bad "$label was accepted $mode"
		else
			ok "$label rejected $mode"
		fi
	done
done <<-'EOF'
	empty root|
	blank root|<SP><SP>
	leading-space root|<SP>/mnt/x
	trailing-space root|/mnt/x<SP>
	relative root|relative/x
	over-byte-budget root|/mnt/kubelet-root-long-enough-to-overrun-the-sun-path-budget-0123456789012345
	one byte over the registrar socket limit|/mnt/kubelet-root-of-exactly-74-bytes-pins-the-socket-budget-xxxxxxxxxxxxx
	multibyte root inside the character limit|/界界界界界界界界界界界界界界界界界界界界界界界界界界界界界界
EOF

# A top-level value differing from kubeletRootDir only in case is refused rather
# than dropped. With the schema on the closed root object catches it; with the
# schema skipped only the template does, and that is where a silent revert to
# /var/lib/kubelet would happen.
while IFS='|' read -r label key; do
	for mode in "with the values schema" "with the values schema skipped"; do
		if [ "$mode" = "with the values schema skipped" ]; then
			set -- --skip-schema-validation
		else
			set --
		fi
		if helm template t "$CHART" "$@" --set-string "$key=/mnt/data/kubelet" >/dev/null 2>&1; then
			bad "$label was accepted $mode"
		else
			ok "$label rejected $mode"
		fi
	done
done <<-'EOF'
	all-lowercase kubeletRootDir|kubeletrootdir
	capitalised kubeletRootDir|KubeletRootDir
	miscased final word|kubeletRootdir
	shouted kubeletRootDir|KUBELETROOTDIR
EOF

# The value the chart still renders correctly, so the guard above cannot be
# passing by refusing everything.
if render "correctly spelled kubeletRootDir with the values schema skipped" \
	--skip-schema-validation --set-string kubeletRootDir=/mnt/data/kubelet; then
	if has '"--kubelet-root-dir=/mnt/data/kubelet"' "$out"; then
		ok "correctly spelled kubeletRootDir still reaches the driver"
	else
		bad "correctly spelled kubeletRootDir did not reach the driver"
	fi
fi

# extraArgs naming the flag: Go's flag package takes the last value and
# extraArgs are spliced in after, so it would win the flag while the mounts kept
# rendering from the chart value. One dash and two are the same flag to it.
while IFS='|' read -r arg want; do
	if helm template t "$CHART" --set-string "extraArgs[0]=$arg" >/dev/null 2>&1; then
		got=accepted
	else
		got=rejected
	fi
	if [ "$got" = "$want" ]; then
		ok "extraArgs '$arg' $got"
	else
		bad "extraArgs '$arg' $got, wanted $want"
	fi
done <<-'EOF'
	--kubelet-root-dir=/other|rejected
	-kubelet-root-dir=/other|rejected
	--kubelet-root-dir|rejected
	-kubelet-root-dir|rejected
	--kubelet-root-directory|accepted
EOF

# Three places have to agree on the default root: the driver's constant, the
# chart value, and the template's test for whether to send the flag. The flag is
# deliberately absent at the default, so a one-sided change would move the mounts
# and leave the driver starting elsewhere, with nothing in the render to say so.
go_default=$(sed -n 's/.*DefaultKubeletRootDir[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' internal/driverconfig/defaults.go)
chart_default=$(yq '.kubeletRootDir' "$CHART/values.yaml")
template_default=$(sed -n 's/.*ne [$]kubeletRoot "\([^"]*\)".*/\1/p' "$CHART/templates/daemonset.yaml")
if [ -n "$go_default" ] && [ "$go_default" = "$chart_default" ] && [ "$go_default" = "$template_default" ]; then
	ok "the default root reads the same in the driver, values.yaml and the template"
else
	bad "the default root differs: driver '$go_default', values.yaml '$chart_default', template '$template_default'"
fi

exit "$fail"
