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
The kubelet root directory, cleaned.

Empty is refused rather than defaulted, and surrounding whitespace refused rather
than trimmed. The mounts below come from this same value, and a trailing space is
legal in a Linux pathname, so trimming would name a directory the kubelet is not
using. Cleaning is kept, since it resolves where the driver's filepath.Join does.

The registrar socket under this root has to fit sun_path, which leaves 107 bytes
for the path and 73 for this value. Checked here in bytes and again in the driver,
which is the one that has to be right, and deliberately not in the values schema:
maxLength counts characters and sees the value before cleaning. The socket name is
spelled out because the chart cannot ask the binary for it; changing driverName in
cmd/dracpu/app.go moves this budget.

An extraArgs entry naming the flag is refused because Go's flag package takes the
last value, so it would win the flag while the paths kept rendering from here.
Matched on the parsed flag name, since the package treats -name and --name alike
and the driver's --help prints the single-dash form.

A top-level value differing from this name only in case is refused rather than
ignored. The schema closes the root object, but --skip-schema-validation turns
that off and this template reads one exact name, so kubeletrootdir would be
dropped and the mounts would render at the default. That is #231 by way of a typo.
*/}}
{{- define "dra-driver-cpu.kubeletRootDir" -}}
{{- range $key, $_ := .Values -}}
{{- if and (ne $key "kubeletRootDir") (eq (lower $key) "kubeletrootdir") -}}
{{- fail (printf "value %q differs from kubeletRootDir only in case, and the chart reads the exact name: it would be ignored and the kubelet paths would render at the default" $key) -}}
{{- end -}}
{{- end -}}
{{- range .Values.extraArgs -}}
{{- if eq "kubelet-root-dir" (. | trimPrefix "--" | trimPrefix "-" | splitList "=" | first) -}}
{{- fail "set kubeletRootDir instead of passing --kubelet-root-dir through extraArgs: the hostPath mounts render from kubeletRootDir, so the flag would move on its own" -}}
{{- end -}}
{{- end -}}
{{- $root := .Values.kubeletRootDir | default "" -}}
{{- if not $root -}}
{{- fail "kubeletRootDir must not be empty" -}}
{{- end -}}
{{- if ne $root (trim $root) -}}
{{- fail (printf "kubeletRootDir must not begin or end with whitespace, got %q: a directory whose name ends in a space is a different directory, so trimming it would point the mounts and the flag together at somewhere the kubelet is not" $root) -}}
{{- end -}}
{{- if not (isAbs $root) -}}
{{- fail (printf "kubeletRootDir must be an absolute path, got %q" $root) -}}
{{- end -}}
{{- $cleaned := clean $root -}}
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
*/}}
{{- define "dra-driver-cpu.pluginsDir" -}}
{{- printf "%s/plugins" (include "dra-driver-cpu.kubeletRootDir" .) | clean -}}
{{- end }}

{{- define "dra-driver-cpu.pluginRegistryDir" -}}
{{- printf "%s/plugins_registry" (include "dra-driver-cpu.kubeletRootDir" .) | clean -}}
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
