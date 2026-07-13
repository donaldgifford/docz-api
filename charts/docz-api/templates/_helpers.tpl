{{/*
Expand the name of the chart.
*/}}
{{- define "docz-api.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this
(by the DNS naming spec). If release name contains chart name it will be used
as a full name.
*/}}
{{- define "docz-api.fullname" -}}
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
{{- define "docz-api.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "docz-api.labels" -}}
helm.sh/chart: {{ include "docz-api.chart" . }}
{{ include "docz-api.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "docz-api.selectorLabels" -}}
app.kubernetes.io/name: {{ include "docz-api.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use.
*/}}
{{- define "docz-api.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "docz-api.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the secret to use.
*/}}
{{- define "docz-api.secretName" -}}
{{- if .Values.secrets.create }}
{{- include "docz-api.fullname" . }}
{{- else }}
{{- required "secrets.existingSecret is required when secrets.create is false" .Values.secrets.existingSecret }}
{{- end }}
{{- end }}

{{/*
Reserved env-var names — keys that the chart already manages on the
Deployment container env list. `templating.vars` may not redeclare any
of these because the chart-emitted entry would shadow the operator's
attempt and produce confusing behavior at runtime.

Returns a space-separated string for has-element style checks.
*/}}
{{- define "docz-api.reservedEnvVars" -}}
GITHUB_APP_ID GITHUB_WEBHOOK_SECRET GITHUB_PRIVATE_KEY GITHUB_PRIVATE_KEY_PATH LISTEN_ADDR METRICS_ADDR LOG_LEVEL DRY_RUN WORKER_COUNT QUEUE_SIZE SCHEDULE_INTERVAL SKIP_FORKS SKIP_ARCHIVED TEMPLATE_DIR WEBHOOK_IP_ALLOWLIST WEBHOOK_IP_ALLOWLIST_FAIL_OPEN TRUST_PROXY_HEADERS GUARDIAN_CONFIG STRICT_TEMPLATES STORE_BACKEND QUEUE_BACKEND SCHEDULER_BACKEND STORE_DSN STORE_POSTGRES_MAX_CONNS QUEUE_VALKEY_DSN JOB_ACK_TIMEOUT REAPER_INTERVAL POD_NAME RECONCILE_FRESHNESS STALE_SWEEP_BATCH_SIZE RATE_LIMIT_RESERVE
{{- end }}

{{/*
Resource name for the chart-rendered Postgres deployment + Service +
PVC + Secret. Always derived from the release fullname; the chart
does not honour an existingSecret for the *baked* mode (the existing
secret is the operator's signal to use external mode).
*/}}
{{- define "docz-api.postgresFullname" -}}
{{- printf "%s-postgres" (include "docz-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Resource name for the chart-rendered Valkey deployment + Service +
PVC + Secret.
*/}}
{{- define "docz-api.valkeyFullname" -}}
{{- printf "%s-valkey" (include "docz-api.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Secret name holding STORE_DSN. Three modes:
  - `external`: operator's existingSecret (required).
  - `baked`:    chart-rendered Secret (postgresFullname).
  - `cnpg`:     CNPG-created `<cluster>-app` Secret.
*/}}
{{- define "docz-api.storeSecretName" -}}
{{- if eq .Values.store.postgres.mode "external" -}}
{{- required "store.postgres.existingSecret is required when store.postgres.mode=external" .Values.store.postgres.existingSecret -}}
{{- else if eq .Values.store.postgres.mode "cnpg" -}}
{{- printf "%s-app" (include "docz-api.postgresFullname" .) -}}
{{- else -}}
{{- include "docz-api.postgresFullname" . -}}
{{- end -}}
{{- end -}}

{{/*
Secret key holding STORE_DSN. CNPG always writes connection strings
under the `uri` key; baked uses the chart-controlled `STORE_DSN`;
external honours `existingSecretKey`.
*/}}
{{- define "docz-api.storeSecretKey" -}}
{{- if eq .Values.store.postgres.mode "external" -}}
{{- .Values.store.postgres.existingSecretKey | default "STORE_DSN" -}}
{{- else if eq .Values.store.postgres.mode "cnpg" -}}
uri
{{- else -}}
STORE_DSN
{{- end -}}
{{- end -}}

{{/*
Secret name holding QUEUE_VALKEY_DSN.
*/}}
{{- define "docz-api.queueSecretName" -}}
{{- if eq .Values.queue.valkey.mode "external" -}}
{{- required "queue.valkey.existingSecret is required when queue.valkey.mode=external" .Values.queue.valkey.existingSecret -}}
{{- else -}}
{{- include "docz-api.valkeyFullname" . -}}
{{- end -}}
{{- end -}}

{{/*
Secret key holding QUEUE_VALKEY_DSN.
*/}}
{{- define "docz-api.queueSecretKey" -}}
{{- if eq .Values.queue.valkey.mode "external" -}}
{{- .Values.queue.valkey.existingSecretKey | default "QUEUE_VALKEY_DSN" -}}
{{- else -}}
QUEUE_VALKEY_DSN
{{- end -}}
{{- end -}}

{{/*
Validates that none of the keys in .Values.templating.vars collide with
chart-managed env vars. Calls `fail` with a clear list of offenders so
the helm-render step exits with a useful error instead of silently
shadowing the chart's own env entries.

Renders empty on success; failure aborts the entire template render.
*/}}
{{- define "docz-api.validateTemplatingVars" -}}
{{- $reserved := splitList " " (trim (include "docz-api.reservedEnvVars" .)) -}}
{{- $offenders := list -}}
{{- range $k, $_ := .Values.templating.vars -}}
{{- if has $k $reserved -}}
{{- $offenders = append $offenders $k -}}
{{- end -}}
{{- end -}}
{{- if $offenders -}}
{{- fail (printf "templating.vars keys collide with chart-managed env vars: %s" (join ", " $offenders)) -}}
{{- end -}}
{{- end }}
