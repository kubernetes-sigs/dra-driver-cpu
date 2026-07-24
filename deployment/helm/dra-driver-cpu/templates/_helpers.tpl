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
Validated kubelet root directory.

The driver derives <root>/plugins_registry (registrar socket) and
<root>/plugins/<driver-name> (plugin data dir) from the kubelet root, so the
host paths and mounts must match filepath.Join on the driver side. The value
defaults to /var/lib/kubelet and is cleaned so it matches the driver's
filepath.Clean. A relative value is rejected here, mirroring the driver's own
validation, so it fails at template time rather than at startup.
*/}}
{{- define "dra-driver-cpu.kubeletRootDir" -}}
{{- $root := .Values.kubeletRootDir | default "" -}}
{{- if and $root (not (isAbs $root)) -}}
{{- fail "kubeletRootDir must be an absolute path" -}}
{{- end -}}
{{- $root | default "/var/lib/kubelet" | clean -}}
{{- end }}

{{/*
Host and mount path for the kubelet plugins directory (<root>/plugins).
*/}}
{{- define "dra-driver-cpu.pluginsDir" -}}
{{- printf "%s/plugins" (include "dra-driver-cpu.kubeletRootDir" .) | clean -}}
{{- end }}

{{/*
Host and mount path for the kubelet plugin registry (<root>/plugins_registry).
*/}}
{{- define "dra-driver-cpu.pluginRegistryDir" -}}
{{- printf "%s/plugins_registry" (include "dra-driver-cpu.kubeletRootDir" .) | clean -}}
{{- end }}
