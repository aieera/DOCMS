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
Common environment variables injected into every Go service
*/}}
{{- define "sedoc.commonEnv" -}}
- name: SEDOC_ENVIRONMENT
  value: {{ .Values.global.tenantIsolation | default "shared" | quote }}
- name: SEDOC_REGION
  value: {{ .Values.global.s3.region | quote }}
- name: SEDOC_DATABASE_URL
  value: "postgresql://{{ .Values.global.database.username }}:$(DB_PASSWORD)@{{ .Values.global.database.host }}:{{ .Values.global.database.port }}/{{ .Values.global.database.name }}?sslmode=require"
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.database.existingSecret }}
      key: {{ .Values.global.database.passwordKey }}
- name: SEDOC_REDIS_URL
  value: "{{ .Values.global.redis.host }}:{{ .Values.global.redis.port }}"
- name: SEDOC_NATS_URL
  value: {{ .Values.global.nats.url | quote }}
# §3.1 / B2.3 — shared gateway-signature secret. Every Go backend's
# pkg/middleware.RequireGatewaySignature() rejects traffic without
# X-Gateway-Signature=$this. Sourced from the same Secret the gateway
# Deployment reads.
- name: SEDOC_GATEWAY_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.gateway.secret.name | default "sedoc-gateway" }}
      key:  {{ .Values.gateway.secret.sharedSecretKey | default "shared-secret" }}
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
- name: SEDOC_LOCAL_KEK
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.encryption.existingSecret }}
      key: local-kek
      optional: true
# Service-to-service key (X-Service-Key). Preview reads it to gate its
# internal /watermark stamping endpoints; the document service reads it
# as the PreviewClient credential. Both must share this value, so it is
# in commonEnv. Optional so a missing secret leaves the preview client
# disabled instead of crash-looping.
- name: SEDOC_SERVICE_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.serviceApiKey.existingSecret }}
      key: {{ .Values.global.serviceApiKey.key }}
      optional: true
# In-cluster preview API base URL for the document watermark client.
- name: SEDOC_PREVIEW_URL
  value: {{ .Values.global.previewURL | default "http://sedoc-preview:8080" | quote }}
{{- end }}

{{/*
Billing-only environment variables (Stripe + internal API key). Included
by the billing service deployment only.
*/}}
{{- define "sedoc.billingEnv" -}}
- name: SEDOC_INTERNAL_API_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.billing.existingSecret }}
      key: {{ .Values.global.billing.internalAPIKeyKey }}
- name: STRIPE_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.billing.existingSecret }}
      key: {{ .Values.global.billing.stripeWebhookSecretKey }}
{{- end }}
