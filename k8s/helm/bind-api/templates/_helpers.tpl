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
{{- if .Values.serviceAccount }}
{{- if .Values.serviceAccount.create }}
{{- default (include "bind-api.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- else }}
{{- default "default" }}
{{- end }}
{{- end }}

{{/*
Master / replica enabled flags.
Роль больше не управляется .Values.role. Наличие набора определяется
replicaCount.master / replicaCount.replica:
  0 -> набор не создаётся.
*/}}
{{- define "bind-api.masterEnabled" -}}
{{- if gt (int (.Values.replicaCount).master) 0 }}true{{- end }}
{{- end }}

{{- define "bind-api.replicaEnabled" -}}
{{- if gt (int (.Values.replicaCount).replica) 0 }}true{{- end }}
{{- end }}

{{/*
Return the appropriate API port
*/}}
{{- define "bind-api.apiPort" -}}
{{- $port := "8080" }}
{{- if .Values.config }}
{{- if .Values.config.apiPort }}
{{- $port = .Values.config.apiPort | trimPrefix ":" }}
{{- end }}
{{- end }}
{{- $port }}
{{- end }}

{{/*
Master URL for replicas.
Указывает на ClusterIP-сервис API мастера (<fullname>-api).
Это обычный Service, а не headless-под, поэтому адрес переживает
пересоздание пода мастера и не зависит от имени релиза.
*/}}
{{- define "bind-api.masterUrl" -}}
{{- if and .Values.master .Values.master.url }}
{{- .Values.master.url }}
{{- else }}
{{- printf "http://%s-api.%s.svc.cluster.local:%s"
  (include "bind-api.fullname" .)
  .Release.Namespace
  (include "bind-api.apiPort" .) }}
{{- end }}
{{- end }}

{{/*
Адрес мастера для директивы BIND masters { } (источник AXFR).
*/}}
{{- define "bind-api.replicaMasterIP" -}}
{{- if and .Values.master .Values.master.ip }}
{{- .Values.master.ip }}
{{- else }}
{{- printf "%s-api.%s.svc.cluster.local" (include "bind-api.fullname" .) .Release.Namespace }}
{{- end }}
{{- end }}

{{/*
PostgreSQL host — привязан к fullname, а не к имени релиза.
*/}}
{{- define "bind-api.postgresqlHost" -}}
{{- if .Values.postgresql.enabled }}
{{- printf "%s-postgresql" (include "bind-api.fullname" .) }}
{{- else if .Values.externalPostgresql.enabled }}
{{- .Values.externalPostgresql.host }}
{{- else }}
{{- "localhost" }}
{{- end }}
{{- end }}

{{/*
Storage class: приоритет global.storageClass, затем zonesStorage.storageClass.
*/}}
{{- define "bind-api.storageClass" -}}
{{- if and .Values.global .Values.global.storageClass }}
{{- .Values.global.storageClass }}
{{- else if and .Values.zonesStorage .Values.zonesStorage.storageClass }}
{{- .Values.zonesStorage.storageClass }}
{{- end }}
{{- end }}

{{/*
BIND allow-recursion string (справочно; сам named.conf формируется в configmap.yaml).
*/}}
{{- define "bind-api.bindAllowRecursion" -}}
{{- $list := list }}
{{- if .Values.bind }}
{{- if .Values.bind.config }}
{{- if .Values.bind.config.allowRecursion }}
{{- range .Values.bind.config.allowRecursion }}
{{- $list = append $list (printf "{ %s; }" .) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- if $list }}
{{- join "; " $list }}
{{- else }}
{{- "{ 10.0.0.0/8; 172.16.0.0/12; 192.168.0.0/16; }" }}
{{- end }}
{{- end }}

{{/*
BIND allow-transfer string (справочно).
*/}}
{{- define "bind-api.bindAllowTransfer" -}}
{{- $list := list }}
{{- if .Values.bind }}
{{- if .Values.bind.config }}
{{- if .Values.bind.config.allowTransfer }}
{{- range .Values.bind.config.allowTransfer }}
{{- $list = append $list (printf "{ %s; }" .) }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- if $list }}
{{- join "; " $list }}
{{- else }}
{{- "{ 10.0.0.0/8; }" }}
{{- end }}
{{- end }}