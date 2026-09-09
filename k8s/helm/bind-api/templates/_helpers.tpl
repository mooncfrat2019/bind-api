{{/*
Expand the name of the chart.
*/}}
{{- define "bind-api.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "bind-api.fullname" -}}
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
{{- define "bind-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "bind-api.labels" -}}
helm.sh/chart: {{ include "bind-api.chart" . }}
{{ include "bind-api.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "bind-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bind-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "bind-api.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "bind-api.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Return the appropriate API port
*/}}
{{- define "bind-api.apiPort" -}}
{{- .Values.config.apiPort | trimPrefix ":" }}
{{- end }}

{{/*
Return the master URL for replicas
*/}}
{{- define "bind-api.masterUrl" -}}
{{- if .Values.master.url }}
{{- .Values.master.url }}
{{- else }}
{{- printf "http://%s-master-0.%s-master.%s.svc.cluster.local:%s"
  .Release.Name
  .Release.Name
  .Release.Namespace
  (include "bind-api.apiPort" .) }}
{{- end }}
{{- end }}

{{/*
PostgreSQL host
*/}}
{{- define "bind-api.postgresqlHost" -}}
{{- if .Values.postgresql.enabled }}
{{- printf "%s-postgresql.%s.svc.cluster.local" .Release.Name .Release.Namespace }}
{{- else }}
{{- .Values.externalPostgresql.host }}
{{- end }}
{{- end }}

{{/*
Storage class
*/}}
{{- define "bind-api.storageClass" -}}
{{- if .Values.global.storageClass }}
{{- .Values.global.storageClass }}
{{- else if .Values.zonesStorage.storageClass }}
{{- .Values.zonesStorage.storageClass }}
{{- else }}
{{- default "" }}
{{- end }}
{{- end }}

{{/*
BIND allow-recursion string
*/}}
{{- define "bind-api.bindAllowRecursion" -}}
{{- $list := list }}
{{- range .Values.bind.config.allowRecursion }}
{{- $list = append $list (printf "{ %s; }" .) }}
{{- end }}
{{- join "; " $list }}
{{- end }}

{{/*
BIND allow-transfer string
*/}}
{{- define "bind-api.bindAllowTransfer" -}}
{{- $list := list }}
{{- range .Values.bind.config.allowTransfer }}
{{- $list = append $list (printf "{ %s; }" .) }}
{{- end }}
{{- join "; " $list }}
{{- end }}