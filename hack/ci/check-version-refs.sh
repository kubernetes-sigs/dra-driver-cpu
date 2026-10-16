#!/bin/bash
#
# Check that the documentation does not name a release older than the one in
# VERSION.
#
# Cutting a release moves VERSION on and leaves the references behind it stale:
# a heading that labels a section with a release, a `tag:` in an example, and a
# self-referential URL pinned to a tag are all correct when they are written and
# quietly wrong afterwards. Nothing about the file changes to give it away,
# which is what makes a check worth having rather than a convention.
#
# A reference that names another release on purpose - an upgrade guide, the
# chart appVersion that trails VERSION - belongs in version-refs-exempt.txt,
# with the reason it is allowed to be there.

set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "${root}"

version=$(tr -d '[:space:]' <VERSION)
release=${version#v}

# Named relative to the repository root so the message reads the same in a
# local run and in a CI log.
exempt_ref="hack/ci/version-refs-exempt.txt"
exempt_file="${root}/${exempt_ref}"

# The documents a user reads: docs/ is published as the documentation site and
# read on GitHub, and the chart's README is rendered from this template.
documents=('docs/**/*.md' README.md 'deployment/helm/*/README.md.gotmpl')

# older <version> <release>: whether <version> is an earlier release of the same
# major version. Comparing within a major version is what keeps a Kubernetes
# release named in the same document out of the comparison.
older() {
	local ver=$1 rel=$2
	local ver_rest=${ver#*.} rel_rest=${rel#*.}
	local ver_major=${ver%%.*} rel_major=${rel%%.*}
	local ver_minor=${ver_rest%%.*} rel_minor=${rel_rest%%.*}
	local ver_patch=${ver_rest#*.} rel_patch=${rel_rest#*.}

	if [ "${ver_major}" != "${rel_major}" ]; then
		return 1
	fi
	if [ "${ver_minor}" -ne "${rel_minor}" ]; then
		[ "${ver_minor}" -lt "${rel_minor}" ] && return 0
		return 1
	fi
	[ "${ver_patch}" -lt "${rel_patch}" ]
}

# exempt <path> <line>: whether version-refs-exempt.txt lets this line name
# another release. An entry is a whole file, or a file and a substring the line
# has to contain.
exempt() {
	local path=$1 line=$2 entry want_path want_text

	[ -f "${exempt_file}" ] || return 1
	while IFS= read -r entry; do
		case ${entry} in '' | '#'*) continue ;; esac
		want_path=${entry%%:*}
		if [ "${want_path}" != "${path}" ]; then
			continue
		fi
		if [ "${entry}" = "${path}" ]; then
			return 0
		fi
		want_text=${entry#*:}
		case ${line} in *"${want_text}"*) return 0 ;; esac
	done <"${exempt_file}"
	return 1
}

status=0
while IFS= read -r path; do
	while IFS=: read -r number line; do
		stale=0
		for literal in $(grep -Eo 'v?[0-9]+\.[0-9]+\.[0-9]+' <<<"${line}"); do
			if older "${literal#v}" "${release}"; then
				stale=1
			fi
		done
		if [ "${stale}" -ne 1 ]; then
			continue
		fi
		if exempt "${path}" "${line}"; then
			continue
		fi
		printf '%s:%s names a release older than %s:\n' "${path}" "${number}" "${version}" >&2
		printf '    %s\n' "${line}" >&2
		status=1
	done < <(grep -nE 'v?[0-9]+\.[0-9]+\.[0-9]+' "${path}" || true)
done < <(git ls-files "${documents[@]}")

if [ "${status}" -ne 0 ]; then
	echo "Update the reference to ${version}, or list it in ${exempt_ref} with the reason." >&2
fi
exit "${status}"
