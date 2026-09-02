{{/*
Expand the name of the chart.
*/}}
{{- define "radar.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "radar.fullname" -}}
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
{{- define "radar.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "radar.labels" -}}
helm.sh/chart: {{ include "radar.chart" . }}
{{ include "radar.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "radar.selectorLabels" -}}
app.kubernetes.io/name: {{ include "radar.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "radar.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "radar.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Whether the timeline SQLite database is written to the shared data volume.
*/}}
{{- define "radar.timelinePersisted" -}}
{{- if and .Values.persistence.enabled (eq .Values.timeline.storage "sqlite") -}}
true
{{- end -}}
{{- end }}

{{/*
Whether the API key database is written to the shared data volume. Keys only
exist when auth is on, and without the volume the database lands on the pod's
emptyDir home — every key stops authenticating the moment the pod cycles.
*/}}
{{- define "radar.apiKeysPersisted" -}}
{{- if and .Values.persistence.enabled (ne .Values.auth.mode "none") (dig "apiKeys" "persist" true .Values.auth) -}}
true
{{- end -}}
{{- end }}

{{/*
Path to the API key database on the data volume.
*/}}
{{- define "radar.apiKeysDbPath" -}}
{{- dig "apiKeys" "dbPath" "/data/api-keys.db" .Values.auth -}}
{{- end }}

{{/*
Whether the pod mounts the PVC at /data. Both SQLite databases share one
volume: an RWO volume cannot be split across pods anyway, so a second PVC
would add a provisioning failure mode without buying any concurrency.
*/}}
{{- define "radar.dataVolume" -}}
{{- if or (include "radar.timelinePersisted" .) (include "radar.apiKeysPersisted" .) -}}
true
{{- end -}}
{{- end }}
