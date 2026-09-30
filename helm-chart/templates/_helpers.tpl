{{- define "bandwidth-quota.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 54 | trimSuffix "-" -}}
{{- end -}}

{{- define "bandwidth-quota.fullname" -}}
{{- $fullname := .Values.fullnameOverride -}}
{{- if not $fullname -}}
{{- $name := include "bandwidth-quota.name" . -}}
{{- if contains $name .Release.Name -}}
{{- $fullname = .Release.Name -}}
{{- else -}}
{{- $fullname = printf "%s-%s" .Release.Name $name -}}
{{- end -}}
{{- end -}}
{{- if not (regexMatch "^[a-z]" $fullname) -}}
{{- $fullname = printf "quota-%s" $fullname -}}
{{- end -}}
{{- $fullname | trunc 54 | trimSuffix "-" -}}
{{- end -}}

{{- define "bandwidth-quota.selectorLabels" -}}
app.kubernetes.io/name: {{ include "bandwidth-quota.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "bandwidth-quota.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{ include "bandwidth-quota.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "bandwidth-quota.redisAddress" -}}
{{- if .Values.redis.enabled -}}
{{- printf "%s-redis:%v" (include "bandwidth-quota.fullname" .) .Values.redis.port -}}
{{- else -}}
{{- required "externalRedis.address is required when redis.enabled=false" .Values.externalRedis.address -}}
{{- end -}}
{{- end -}}
