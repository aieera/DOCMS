{{/*
Chart name + fully-qualified release name. Standard Helm boilerplate —
these were referenced by templates/gateway/* (sedoc.fullname / sedoc.name)
but never defined, so `helm template`/`helm install` failed for the WHOLE
chart with "no template sedoc.fullname". Defining them makes the chart
render (and unblocks the env golden test below).
*/}}
{{- define "sedoc.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- define "sedoc.fullname" -}}
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
Common labels
*/}}
{{- define "sedoc.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "sedoc.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image reference
*/}}
{{- define "sedoc.image" -}}
{{ .Values.global.imageRegistry }}/{{ .svcName }}:{{ .Values.global.imageTag }}
{{- end }}

{{/*
Database host. Explicit global.database.host wins; otherwise the bundled
database: the CloudNativePG read-write Service when postgresql.useOperator,
else the Bitnami subchart Service. (Empty used to render a DSN with no host.)
*/}}
{{- define "sedoc.dbHost" -}}
{{- if .Values.global.database.host -}}
{{- .Values.global.database.host -}}
{{- else if .Values.postgresql.useOperator -}}
{{- printf "%s-postgres-rw" .Release.Name -}}
{{- else -}}
{{- printf "%s-postgresql" .Release.Name -}}
{{- end -}}
{{- end }}

{{/*
Redis host: explicit global.redis.host, else the bundled Bitnami master.
*/}}
{{- define "sedoc.redisHost" -}}
{{- .Values.global.redis.host | default (printf "%s-redis-master" .Release.Name) -}}
{{- end }}

{{/*
SEDOC_ALLOWED_ORIGINS for collaboration: explicit
collaboration.allowedOrigins, else https://<global.domain>.
*/}}
{{- define "sedoc.allowedOrigins" -}}
{{- .Values.collaboration.allowedOrigins | default (printf "https://%s" .Values.global.domain) -}}
{{- end }}

{{/*
Core environment shared by every workload that talks to the platform's data
and identity planes (Go services via commonEnv, collaboration directly):
environment, region, client-IP trust, database, NATS, gateway secret and
internal key. $(VAR) references only expand variables defined EARLIER in
the list, so the password entries come first.
*/}}
{{- define "sedoc.coreEnv" -}}
- name: SEDOC_ENVIRONMENT
  value: {{ .Values.global.environment | quote }}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.database.existingSecret }}
      key: {{ .Values.global.database.passwordKey }}
- name: SEDOC_DATABASE_URL
  value: "postgresql://{{ .Values.global.database.username }}:$(DB_PASSWORD)@{{ include "sedoc.dbHost" . }}:{{ .Values.global.database.port }}/{{ .Values.global.database.name }}?sslmode={{ .Values.global.database.sslMode }}"
- name: SEDOC_NATS_URL
  value: {{ tpl .Values.global.nats.url . | quote }}
- name: SEDOC_GATEWAY_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.gateway.secret.name }}
      key:  {{ .Values.gateway.secret.sharedSecretKey }}
{{- end }}

{{/*
Common environment variables injected into every Go service
*/}}
{{- define "sedoc.commonEnv" -}}
{{- include "sedoc.coreEnv" . }}
- name: SEDOC_REGION
  value: {{ .Values.global.s3.region | quote }}
# R-19 — REDIS_PASSWORD before every URL that embeds it.
- name: REDIS_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.redis.existingSecret }}
      key: {{ .Values.global.redis.passwordKey }}
- name: SEDOC_REDIS_URL
  value: "{{ include "sedoc.redisHost" . }}:{{ .Values.global.redis.port }}"
- name: SEDOC_REDIS_PASSWORD
  value: "$(REDIS_PASSWORD)"
# Object storage. Canonical env spelling is SEDOC_S3_* — that's what
# compose + ansible inject, what the Python services (preview,
# intelligence) read via pydantic, and the PREFERRED binding in the Go
# pkg/config (which accepts SEDOC_MINIO_* only as a legacy fallback).
# This chart used to inject SEDOC_MINIO_*, which the Python services
# never read — so the preview/intelligence boto3 clients had no creds in
# cluster. Emit SEDOC_S3_* so every service, Go and Python, gets them.
- name: SEDOC_S3_ENDPOINT
  value: {{ .Values.global.s3.endpoint | quote }}
- name: SEDOC_S3_USE_SSL
  value: {{ .Values.global.s3.useSSL | quote }}
- name: SEDOC_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.s3.accessKeySecret }}
      key: {{ .Values.global.s3.accessKeyKey }}
- name: SEDOC_S3_SECRET_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.s3.accessKeySecret }}
      key: {{ .Values.global.s3.secretKeyKey }}
- name: OPENSEARCH_URL
  value: {{ .Values.global.openSearchURL | default .Values.global.opensearch.url | quote }}
- name: TEMPORAL_ADDR
  value: {{ .Values.global.temporalAddr | default .Values.global.temporal.address | quote }}
- name: CLAMAV_ADDR
  value: {{ .Values.global.clamAVAddr | quote }}
- name: POLICY_SERVICE_ADDR
  value: {{ .Values.global.policyServiceAddr | quote }}
- name: SEDOC_PUBLIC_URL
  value: {{ tpl (.Values.global.publicURL | default (printf "https://%s" .Values.global.domain)) . | quote }}
- name: SEDOC_S3_PUBLIC_BASE
  value: {{ .Values.global.s3PublicBase | default "" | quote }}
# H-20 — required (optional: false): a missing KEK must stop the rollout,
# not let storage write plaintext.
- name: SEDOC_LOCAL_KEK
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.encryption.existingSecret }}
      key: {{ .Values.global.encryption.key }}
      optional: false
# DEC-3 internal-service key (X-Internal-Service-Key) — every service, same
# value.
- name: SEDOC_INTERNAL_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.platformSecrets.existingSecret }}
      key: {{ .Values.global.platformSecrets.internalAPIKeyKey }}
# Service-to-service key (X-Service-Key) for the document -> preview
# watermark call; preview gates its stamping endpoints on it.
- name: SEDOC_SERVICE_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.serviceApiKey.existingSecret }}
      key: {{ .Values.global.serviceApiKey.key }}
# In-cluster preview API base URL for the document watermark client.
- name: SEDOC_PREVIEW_URL
  value: {{ .Values.global.previewURL | default "http://sedoc-preview:8080" | quote }}
{{- end }}

{{/*
R-18 — every (Secret, key) the chart's pods read, as YAML
{secretName: [keys]}. The ExternalSecrets are generated from this, so ESO
can neither miss a key a pod needs nor create one nothing reads.
tests/check_rendered.py (secret-alignment) cross-checks the rendered pods.
Optional references (e.g. the audit signing key) are deliberately absent.
*/}}
{{- define "sedoc.secretInventory" -}}
{{- $v := .Values -}}
{{- $refs := list
  (list $v.gateway.secret.name $v.gateway.secret.sharedSecretKey)
  (list $v.global.encryption.existingSecret $v.global.encryption.key)
  (list $v.global.platformSecrets.existingSecret $v.global.platformSecrets.internalAPIKeyKey)
  (list $v.global.serviceApiKey.existingSecret $v.global.serviceApiKey.key)
  (list $v.global.database.existingSecret $v.global.database.passwordKey)
  (list $v.global.redis.existingSecret $v.global.redis.passwordKey)
  (list $v.global.s3.accessKeySecret $v.global.s3.accessKeyKey)
  (list $v.global.s3.accessKeySecret $v.global.s3.secretKeyKey)
-}}
{{- if $v.migrations.enabled }}
{{- $refs = append $refs (list $v.global.database.migration.existingSecret $v.global.database.migration.passwordKey) }}
{{- end }}
{{- if $v.document.enabled }}
{{- $refs = append $refs (list $v.global.platformSecrets.existingSecret $v.global.platformSecrets.irmSigningKeyKey) }}
{{- $refs = append $refs (list $v.global.platformSecrets.existingSecret $v.global.platformSecrets.ztTokenPepperKey) }}
{{- $refs = append $refs (list $v.global.platformSecrets.existingSecret $v.global.platformSecrets.onlyofficeJWTKey) }}
{{- end }}
{{- if $v.billing.enabled }}
{{- $refs = append $refs (list $v.global.billing.existingSecret $v.global.billing.stripeWebhookSecretKey) }}
{{- end }}
{{- if and $v.signature.enabled $v.signatureSigner.enabled }}
{{- $refs = append $refs (list $v.signatureSigner.keystore.existingSecret $v.signatureSigner.keystore.fileKey) }}
{{- $refs = append $refs (list $v.signatureSigner.keystore.existingSecret $v.signatureSigner.keystore.passwordKey) }}
{{- end }}
{{- if $v.soc2Evidence.enabled }}
{{- $refs = append $refs (list $v.global.database.evidence.existingSecret $v.global.database.evidence.passwordKey) }}
{{- $refs = append $refs (list $v.soc2Evidence.secret.name $v.soc2Evidence.secret.signingKeyKey) }}
{{- end }}
{{- if $v.backup.enabled }}
{{- $refs = append $refs (list $v.global.database.backup.existingSecret $v.global.database.backup.passwordKey) }}
{{- end }}
{{- $inv := dict -}}
{{- range $refs }}
{{- $name := index . 0 }}{{ $key := index . 1 }}
{{- $keys := get $inv $name | default list }}
{{- if not (has $key $keys) }}{{ $_ := set $inv $name (append $keys $key) }}{{ end }}
{{- end }}
{{- toYaml $inv }}
{{- end }}

{{/*
C-1 — deploy/gateway/kong.yaml with compose upstream hosts rewritten to the
chart's Service names (url: http://<svc>:<port> -> http://sedoc-<svc>:<port>).
*/}}
{{- define "sedoc.kongConfig" -}}
{{- $src := required "files/kong.yaml is missing from the chart" (.Files.Get "files/kong.yaml") -}}
{{- regexReplaceAll "url: http://([a-z0-9-]+):" $src "url: http://sedoc-${1}:" -}}
{{- end }}

{{/*
R-15 — container securityContext satisfying the `restricted` Pod Security
Standard (non-root, no privilege escalation, no capabilities, RuntimeDefault
seccomp) with a read-only root filesystem. Every workload the chart owns
uses it (or the equivalent inline block); writable paths are emptyDirs.
*/}}
{{- define "sedoc.restrictedSecurityContext" -}}
runAsNonRoot: true
runAsUser: 1000
runAsGroup: 1000
readOnlyRootFilesystem: true
allowPrivilegeEscalation: false
capabilities:
  drop: ["ALL"]
seccompProfile:
  type: RuntimeDefault
{{- end }}

{{/*
redis://:PASSWORD@host:port/<db> for the Python / Node clients. Requires
REDIS_PASSWORD earlier in the env list (commonEnv defines it).
*/}}
{{- define "sedoc.redisURL" -}}
redis://:$(REDIS_PASSWORD)@{{ include "sedoc.redisHost" .ctx }}:{{ .ctx.Values.global.redis.port }}/{{ .db }}
{{- end }}
