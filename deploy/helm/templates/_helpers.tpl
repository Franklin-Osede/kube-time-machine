{{/*
Chart name (overridable so users can shorten "kube-time-machine" if they want).
*/}}
{{- define "ktm.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified resource name: "<release>-<name>", truncated to 63 chars.
*/}}
{{- define "ktm.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Chart label, used for the standard `helm.sh/chart` annotation.
*/}}
{{- define "ktm.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Standard labels applied to every object the chart owns.
*/}}
{{- define "ktm.labels" -}}
helm.sh/chart: {{ include "ktm.chart" . }}
{{ include "ktm.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: agent
app.kubernetes.io/part-of: kube-time-machine
{{- end -}}

{{/*
Selector labels: the subset that must be stable across upgrades.
*/}}
{{- define "ktm.selectorLabels" -}}
app.kubernetes.io/name: {{ include "ktm.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
ServiceAccount name. Defaults to the fullname; overridable.
*/}}
{{- define "ktm.serviceAccountName" -}}
{{- default (include "ktm.fullname" .) .Values.serviceAccount.name -}}
{{- end -}}

{{/*
Resolved image reference. Tag falls back to .Chart.AppVersion when empty.
*/}}
{{- define "ktm.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}
