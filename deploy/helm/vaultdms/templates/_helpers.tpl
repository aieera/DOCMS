{{/*
Common labels
*/}}
{{- define "vaultdms.labels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "vaultdms.selectorLabels" -}}
app.kubernetes.io/name: {{ .name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Image reference
*/}}
{{- define "vaultdms.image" -}}
{{ .Values.global.imageRegistry }}/{{ .svcName }}:{{ .Values.global.imageTag }}
{{- end }}

{{/*
Common environment variables injected into every Go service
*/}}
{{- define "vaultdms.commonEnv" -}}
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
      name: {{ .Values.gateway.secret.name | default "vaultdms-gateway" }}
      key:  {{ .Values.gateway.secret.sharedSecretKey | default "shared-secret" }}
- name: SEDOC_MINIO_ENDPOINT
  value: {{ .Values.global.s3.endpoint | quote }}
- name: SEDOC_MINIO_USE_SSL
  value: {{ .Values.global.s3.useSSL | quote }}
- name: SEDOC_MINIO_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.s3.accessKeySecret }}
      key: {{ .Values.global.s3.accessKeyKey }}
- name: SEDOC_MINIO_SECRET_KEY
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
{{- end }}

{{/*
Billing-only environment variables (Stripe + internal API key). Included
by the billing service deployment only.
*/}}
{{- define "vaultdms.billingEnv" -}}
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
