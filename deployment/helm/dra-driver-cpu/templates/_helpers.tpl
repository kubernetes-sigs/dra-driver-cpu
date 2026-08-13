{{/*
Expand the name of the chart.
*/}}
{{- define "dra-driver-cpu.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "dra-driver-cpu.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "dra-driver-cpu.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "dra-driver-cpu.labels" -}}
helm.sh/chart: {{ include "dra-driver-cpu.chart" . }}
{{ include "dra-driver-cpu.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "dra-driver-cpu.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dra-driver-cpu.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
The kubelet root directory, cleaned and validated. See the comment above each
check below for why that specific check exists.
*/}}
{{- define "dra-driver-cpu.kubeletRootDir" -}}
{{- /* See #231: Helm looks this up by exact name, so a key differing only in
       case is ignored and the mounts would render at the default. */ -}}
{{- range $key, $_ := .Values -}}
{{- if and (ne $key "kubeletRootDir") (eq (lower $key) "kubeletrootdir") -}}
{{- fail (printf "value %q differs from kubeletRootDir only in case, and the chart reads the exact name: it would be ignored and the kubelet paths would render at the default" $key) -}}
{{- end -}}
{{- end -}}
{{- /* An upgrade run with --reuse-values carries the values the release was
       installed with, so a release older than this value has no key here at
       all. Absent reads as the default, and so does null, which Helm drops
       while coalescing; an explicit empty string still does not. */ -}}
{{- $root := "/var/lib/kubelet" -}}
{{- if hasKey .Values "kubeletRootDir" -}}
{{- $root = .Values.kubeletRootDir | default "" -}}
{{- end -}}
{{- if not $root -}}
{{- fail "kubeletRootDir must not be empty" -}}
{{- end -}}
{{- if ne $root (trim $root) -}}
{{- fail (printf "kubeletRootDir must not begin or end with whitespace, got %q: a directory whose name ends in a space is a different directory, so trimming it would point the mounts and the flag together at somewhere the kubelet is not" $root) -}}
{{- end -}}
{{- if not (isAbs $root) -}}
{{- fail (printf "kubeletRootDir must be an absolute path, got %q" $root) -}}
{{- end -}}
{{- if or (contains "$(" $root) (contains "$$" $root) -}}
{{- fail (printf "kubeletRootDir must not contain %q or %q, got %q: the kubelet expands those in a container's arguments and not in its mount paths, so the spelling is refused wherever it appears rather than only where it would still diverge after cleaning" "$(" "$$" $root) -}}
{{- end -}}
{{- /* Cleaned (not just trimmed) so this resolves to the same directory the
       driver's filepath.Join produces. */ -}}
{{- $cleaned := clean $root -}}
{{- /* The registrar socket under this root has to fit sun_path: 108 bytes minus
       the terminating NUL leaves 107 for the path, and this suffix, fixed while
       rolling updates are off, leaves 73 for the root. Checked here in bytes,
       and again in the driver, which is the one that has to be right --
       deliberately not in the values schema, since maxLength counts characters
       and sees the value before cleaning. */ -}}
{{- $socket := clean (printf "%s/plugins_registry/dra.cpu-reg.sock" $cleaned) -}}
{{- if gt (len $socket) 107 -}}
{{- fail (printf "kubelet registrar socket path %q is %d bytes, over the 107-byte limit for a Unix socket path: kubeletRootDir has 73 bytes to spend and is using %d" $socket (len $socket) (len $cleaned)) -}}
{{- end -}}
{{- $cleaned -}}
{{- end }}

{{/*
The two directories under the kubelet root, cleaned as whole paths rather than
appended to it at each use. A root of "/" is what makes the difference visible:
appending gives "//plugins", while the driver derives "/plugins" through
filepath.Join, so the chart and the driver would agree only because Linux
collapses a leading double slash. The invariant this value exists for is that
both name the same path, and it should hold in the text rather than in what the
kernel is willing to overlook.

Each takes the already-validated root string (from "dra-driver-cpu.kubeletRootDir")
rather than the whole context, so callers compute and validate the root once per
render and pass it in, instead of re-running validation on every mount/hostPath.
*/}}
{{- define "dra-driver-cpu.pluginsDir" -}}
{{- printf "%s/plugins" . | clean -}}
{{- end }}

{{- define "dra-driver-cpu.pluginRegistryDir" -}}
{{- printf "%s/plugins_registry" . | clean -}}
{{- end }}

{{/*
Effective driverConfig: folds deprecated args.* fields (cpuDeviceMode,
groupBy, reservedCPUs, hostnameOverride) into driverConfig, args.* wins on
conflicts.
*/}}
{{- define "dra-driver-cpu.effectiveDriverConfig" -}}
{{- $cfg := deepCopy (.Values.driverConfig | default dict) -}}
{{- if .Values.args.cpuDeviceMode }}
{{- $_ := set $cfg "cpuDeviceMode" .Values.args.cpuDeviceMode }}
{{- end }}
{{- if .Values.args.groupBy }}
{{- $_ := set $cfg "groupBy" .Values.args.groupBy }}
{{- end }}
{{- if .Values.args.reservedCPUs }}
{{- $_ := set $cfg "reservedCPUs" .Values.args.reservedCPUs }}
{{- end }}
{{- if .Values.args.hostnameOverride }}
{{- $_ := set $cfg "hostnameOverride" .Values.args.hostnameOverride }}
{{- end }}
{{- toYaml $cfg -}}
{{- end }}
