{{/*
Generic Go service deployment template.
Usage: {{ include "sedoc.goServiceDeployment" (dict "svcName" "document" "svc" .Values.document "Values" .Values "Release" .Release "Chart" .Chart) }}
*/}}
{{- define "sedoc.goServiceDeployment" -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: sedoc-{{ .svcName }}
  labels:
    {{- include "sedoc.labels" (dict "name" .svcName "Release" .Release "Chart" .Chart) | nindent 4 }}
spec:
  replicas: {{ .svc.replicas | default 2 }}
  selector:
    matchLabels:
      {{- include "sedoc.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "sedoc.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 8 }}
        app.kubernetes.io/part-of: sedoc
    spec:
      {{- with .Values.global.imagePullSecrets }}
      imagePullSecrets: {{- toYaml . | nindent 8 }}
      {{- end }}
      affinity:
        podAntiAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector:
                  matchLabels:
                    app.kubernetes.io/name: {{ .svcName }}
                topologyKey: kubernetes.io/hostname
      containers:
        - name: {{ .svcName }}
          image: {{ .Values.global.imageRegistry }}/{{ .svcName }}:{{ .Values.global.imageTag }}
          imagePullPolicy: {{ .Values.global.imagePullPolicy }}
          ports:
            {{- if .svc.grpcPort }}
            - name: grpc
              containerPort: {{ .svc.grpcPort }}
            {{- end }}
            {{- if .svc.httpPort }}
            - name: http
              containerPort: {{ .svc.httpPort }}
            {{- end }}
            - name: health
              containerPort: {{ .svc.healthPort | default 8081 }}
          env:
            - name: SEDOC_SERVICE_NAME
              value: {{ .svcName | quote }}
            {{- if .svc.grpcPort }}
            - name: SEDOC_GRPC_PORT
              value: {{ .svc.grpcPort | quote }}
            {{- end }}
            {{- if .svc.httpPort }}
            - name: SEDOC_HTTP_PORT
              value: {{ .svc.httpPort | quote }}
            {{- end }}
            - name: SEDOC_HEALTH_PORT
              value: {{ .svc.healthPort | default 8081 | quote }}
            {{- include "sedoc.commonEnv" (dict "Values" .Values "Release" .Release) | nindent 12 }}
            {{- include "sedoc.serviceEnv" . | nindent 12 }}
            {{- range $k, $v := .svc.env }}
            - name: {{ $k }}
              value: {{ $v | quote }}
            {{- end }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: health
            initialDelaySeconds: 10
            periodSeconds: 15
            timeoutSeconds: 3
          readinessProbe:
            httpGet:
              path: /readyz
              port: health
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 3
          startupProbe:
            httpGet:
              path: /healthz
              port: health
            failureThreshold: 60
            periodSeconds: 1
          volumeMounts:
            # readOnlyRootFilesystem: scratch space for temp files.
            - name: tmp
              mountPath: /tmp
            {{- if eq .svcName "connector" }}
            - name: intake
              mountPath: /data/intake
            {{- end }}
          resources:
            {{- toYaml .svc.resources | nindent 12 }}
          # PodSecurityStandards restricted profile (ADR 0092). The
          # restricted profile requires capabilities.drop=[ALL] and a
          # seccompProfile of RuntimeDefault or Localhost in addition
          # to the runAsNonRoot / readOnlyRootFilesystem we already set.
          # Removing these in dev needs an explicit
          # global.podSecurity.profile=baseline override.
          securityContext:
            runAsNonRoot: true
            runAsUser: 1000
            runAsGroup: 1000
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            seccompProfile:
              type: RuntimeDefault
      volumes:
        - name: tmp
          emptyDir: {}
        {{- if eq .svcName "connector" }}
        # SEDOC_INTAKE_ROOT. An emptyDir keeps the path valid; mount the
        # SMB/NFS share as a PVC (connector.intake.existingClaim) to use
        # watched-folder intake.
        - name: intake
          {{- if .svc.intake.existingClaim }}
          persistentVolumeClaim:
            claimName: {{ .svc.intake.existingClaim }}
          {{- else }}
          emptyDir: {}
          {{- end }}
        {{- end }}
{{- end }}

{{/*
Per-service environment beyond commonEnv (H-18 and the workstream config
needs). One dispatcher so goServiceDeployment stays generic; services
without extras render nothing.
*/}}
{{- define "sedoc.serviceEnv" -}}
{{- if eq .svcName "document" }}
# Required outside dev (DOC3): IRM export, zero-trust tokens and OnlyOffice
# answer 503 without them. Rotating the zt pepper invalidates live zt links.
- name: SEDOC_IRM_SIGNING_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.platformSecrets.existingSecret }}
      key: {{ .Values.global.platformSecrets.irmSigningKeyKey }}
- name: SEDOC_ZT_TOKEN_PEPPER
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.platformSecrets.existingSecret }}
      key: {{ .Values.global.platformSecrets.ztTokenPepperKey }}
- name: SEDOC_ONLYOFFICE_JWT
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.platformSecrets.existingSecret }}
      key: {{ .Values.global.platformSecrets.onlyofficeJWTKey }}
{{- end }}
{{- if eq .svcName "workflow" }}
# Base URLs (no /api/v1): DSR search/connector purges, saved-search alerts,
# the workflow-owned PAdES seal. Unset, those paths no-op or fail.
- name: SEDOC_SEARCH_URL
  value: "http://sedoc-search:8080"
- name: SEDOC_CONNECTOR_URL
  value: "http://sedoc-connector:8080"
- name: SEDOC_SIGNATURE_URL
  value: "http://sedoc-signature:8080"
{{- end }}
{{- if eq .svcName "search" }}
- name: SEDOC_INTELLIGENCE_EMBED_URL
  value: "http://sedoc-intelligence:8080/internal/v1/embed-query"
- name: SEDOC_QDRANT_URL
  value: {{ tpl .Values.global.qdrant.url $ | quote }}
- name: SEDOC_QDRANT_COLLECTION
  value: {{ .Values.global.qdrant.collection | quote }}
{{- end }}
{{- if eq .svcName "connector" }}
- name: SEDOC_INTELLIGENCE_URL
  value: "http://sedoc-intelligence:8080"
# Watched folders must live under <root>/<tenant_id>/ (volume below).
- name: SEDOC_INTAKE_ROOT
  value: "/data/intake"
{{- end }}
{{- if eq .svcName "billing" }}
# Billing refuses to boot in prod without the Stripe webhook secret.
- name: STRIPE_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: {{ .Values.global.billing.existingSecret }}
      key: {{ .Values.global.billing.stripeWebhookSecretKey }}
{{- end }}
{{- if eq .svcName "signature" }}
# ADR 0025 — server seal through the DSS sidecar (signature-signer).
- name: SEDOC_SIGNER
  value: "dss"
- name: SIGNER_ADDR
  value: "sedoc-signature-signer:{{ .Values.signatureSigner.port }}"
- name: SEDOC_DOCUMENT_HTTP_URL
  value: "http://sedoc-document:8080"
# B-T / B-LT need SIGNER_TSA_URL; the service refuses to start otherwise.
- name: SEDOC_SEAL_LEVEL
  value: {{ .svc.sealLevel | quote }}
- name: SIGNER_TSA_URL
  value: {{ .svc.tsaURL | quote }}
{{- end }}
{{- end }}

{{/*
Generic ClusterIP Service
*/}}
{{- define "sedoc.goServiceSvc" -}}
apiVersion: v1
kind: Service
metadata:
  name: sedoc-{{ .svcName }}
  labels:
    {{- include "sedoc.labels" (dict "name" .svcName "Release" .Release "Chart" .Chart) | nindent 4 }}
spec:
  type: ClusterIP
  selector:
    {{- include "sedoc.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 4 }}
  ports:
    {{- if .svc.grpcPort }}
    - name: grpc
      port: {{ .svc.grpcPort }}
      targetPort: grpc
    {{- end }}
    {{- if .svc.httpPort }}
    - name: http
      port: {{ .svc.httpPort }}
      targetPort: http
    {{- end }}
    - name: health
      port: {{ .svc.healthPort | default 8081 }}
      targetPort: health
{{- end }}

{{/*
Generic HPA
*/}}
{{- define "sedoc.goServiceHPA" -}}
{{- if .svc.hpa.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: sedoc-{{ .svcName }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: sedoc-{{ .svcName }}
  minReplicas: {{ .svc.hpa.minReplicas | default 2 }}
  maxReplicas: {{ .svc.hpa.maxReplicas | default 10 }}
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: {{ .svc.hpa.targetCPU | default 70 }}
{{- end }}
{{- end }}

{{/*
Generic PodDisruptionBudget
*/}}
{{- define "sedoc.goServicePDB" -}}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: sedoc-{{ .svcName }}
spec:
  minAvailable: 1
  selector:
    matchLabels:
      {{- include "sedoc.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 6 }}
{{- end }}

{{/*
Generic NetworkPolicy — default deny ingress, allow only needed.
*/}}
{{- define "sedoc.goServiceNetworkPolicy" -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sedoc-{{ .svcName }}
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: {{ .svcName }}
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/instance: {{ .Release.Name }}
      ports:
        {{- if .svc.grpcPort }}
        - port: {{ .svc.grpcPort }}
        {{- end }}
        {{- if .svc.httpPort }}
        - port: {{ .svc.httpPort }}
        {{- end }}
        - port: {{ .svc.healthPort | default 8081 }}
{{- end }}

{{/*
Generic ServiceMonitor for Prometheus
*/}}
{{- define "sedoc.goServiceMonitor" -}}
{{- /* Only when the Prometheus Operator CRD exists — otherwise the whole
       install fails with "no matches for kind ServiceMonitor". */ -}}
{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1/ServiceMonitor" }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: sedoc-{{ .svcName }}
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ .svcName }}
  endpoints:
    - port: health
      path: /metrics
      interval: 30s
{{- end }}
{{- end }}
