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
- name: VAULTDMS_ENVIRONMENT
  value: {{ .Values.global.tenantIsolation | default "shared" | quote }}
- name: VAULTDMS_REGION
  value: {{ .Values.global.s3.region | quote }}
- name: VAULTDMS_DATABASE_URL
  value: "postgresql://{{ .Values.global.database.username }}:$(DB_PASSWORD)@{{ .Values.global.database.host }}:{{ .Values.global.database.port }}/{{ .Values.global.database.name }}?sslmode=require"
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.database.existingSecret }}
      key: {{ .Values.global.database.passwordKey }}
- name: VAULTDMS_REDIS_URL
  value: "{{ .Values.global.redis.host }}:{{ .Values.global.redis.port }}"
- name: VAULTDMS_NATS_URL
  value: {{ .Values.global.nats.url | quote }}
# §3.1 / B2.3 — shared gateway-signature secret. Every Go backend's
# pkg/middleware.RequireGatewaySignature() rejects traffic without
# X-Gateway-Signature=$this. Sourced from the same Secret the gateway
# Deployment reads.
- name: VAULTDMS_GATEWAY_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.gateway.secret.name | default "vaultdms-gateway" }}
      key:  {{ .Values.gateway.secret.sharedSecretKey | default "shared-secret" }}
# ADR 0031 — /internal/* auth plane. Default mode is unset so services
# keep their pre-ADR behaviour (gateway-signature on every path) until
# an operator opts a service in via internalAuth.mode. Empty env var =
# Verifier is nil = internalauth.Mux falls through to the gateway-sig
# middleware.
{{- if .Values.global.internalAuth.mode }}
- name: VAULTDMS_INTERNAL_AUTH_MODE
  value: {{ .Values.global.internalAuth.mode | quote }}
{{- end }}
{{- if ne .Values.global.internalAuth.mode "mtls" }}
- name: VAULTDMS_INTERNAL_HMAC_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.internalAuth.hmacSecret.name | default "vaultdms-internal-auth" }}
      key:  {{ .Values.global.internalAuth.hmacSecret.key  | default "hmac-secret" }}
{{- end }}
{{- if ne .Values.global.internalAuth.mode "hmac" }}
- name: VAULTDMS_INTERNAL_CA_CERT
  value: /etc/vaultdms/internal-mtls/ca.crt
- name: VAULTDMS_INTERNAL_CLIENT_CERT
  value: /etc/vaultdms/internal-mtls/tls.crt
- name: VAULTDMS_INTERNAL_CLIENT_KEY
  value: /etc/vaultdms/internal-mtls/tls.key
{{- with .Values.global.internalAuth.sanAllowlist }}
- name: VAULTDMS_INTERNAL_SAN_ALLOWLIST
  value: {{ join "," . | quote }}
{{- end }}
{{- end }}
# ADR 0031 / T-D-2 — trusted-proxy CIDR list. Empty here would panic
# at boot because global.env == production. Operators MUST override
# this with the ingress/load-balancer CIDRs for the cluster.
- name: VAULTDMS_TRUSTED_PROXY_CIDRS
  value: {{ join "," .Values.global.trustedProxyCIDRs | quote }}
- name: VAULTDMS_ENV
  value: {{ .Values.global.env | default "production" | quote }}
{{- if .Values.global.prometheus.url }}
- name: VAULTDMS_PROMETHEUS_URL
  value: {{ .Values.global.prometheus.url | quote }}
{{- end }}
- name: VAULTDMS_S3_ENDPOINT
  value: {{ .Values.global.s3.endpoint | quote }}
- name: VAULTDMS_S3_USE_SSL
  value: {{ .Values.global.s3.useSSL | quote }}
- name: VAULTDMS_S3_ACCESS_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.s3.accessKeySecret }}
      key: {{ .Values.global.s3.accessKeyKey }}
- name: VAULTDMS_S3_SECRET_KEY
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
- name: VAULTDMS_PUBLIC_URL
  value: {{ tpl (.Values.global.publicURL | default (printf "https://%s" .Values.global.domain)) . | quote }}
- name: VAULTDMS_S3_PUBLIC_BASE
  value: {{ .Values.global.s3PublicBase | default "" | quote }}
- name: VAULTDMS_LOCAL_KEK
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
- name: VAULTDMS_INTERNAL_API_KEY
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
