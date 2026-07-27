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
