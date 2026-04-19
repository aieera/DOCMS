{{/*
Generic Go service deployment template.
Usage: {{ include "vaultdms.goServiceDeployment" (dict "svcName" "document" "svc" .Values.document "Values" .Values "Release" .Release "Chart" .Chart) }}
*/}}
{{- define "vaultdms.goServiceDeployment" -}}
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vaultdms-{{ .svcName }}
  labels:
    {{- include "vaultdms.labels" (dict "name" .svcName "Release" .Release "Chart" .Chart) | nindent 4 }}
spec:
  replicas: {{ .svc.replicas | default 2 }}
  selector:
    matchLabels:
      {{- include "vaultdms.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 6 }}
  template:
    metadata:
      labels:
        {{- include "vaultdms.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 8 }}
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
            - name: VAULTDMS_SERVICE_NAME
              value: {{ .svcName | quote }}
            {{- if .svc.grpcPort }}
            - name: VAULTDMS_GRPC_PORT
              value: {{ .svc.grpcPort | quote }}
            {{- end }}
            {{- if .svc.httpPort }}
            - name: VAULTDMS_HTTP_PORT
              value: {{ .svc.httpPort | quote }}
            {{- end }}
            - name: VAULTDMS_HEALTH_PORT
              value: {{ .svc.healthPort | default 8081 | quote }}
            {{- include "vaultdms.commonEnv" (dict "Values" .Values) | nindent 12 }}
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
          resources:
            {{- toYaml .svc.resources | nindent 12 }}
          securityContext:
            runAsNonRoot: true
            runAsUser: 1000
            readOnlyRootFilesystem: true
            allowPrivilegeEscalation: false
{{- end }}

{{/*
Generic ClusterIP Service
*/}}
{{- define "vaultdms.goServiceSvc" -}}
apiVersion: v1
kind: Service
metadata:
  name: vaultdms-{{ .svcName }}
  labels:
    {{- include "vaultdms.labels" (dict "name" .svcName "Release" .Release "Chart" .Chart) | nindent 4 }}
spec:
  type: ClusterIP
  selector:
    {{- include "vaultdms.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 4 }}
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
{{- define "vaultdms.goServiceHPA" -}}
{{- if .svc.hpa.enabled }}
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: vaultdms-{{ .svcName }}
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: vaultdms-{{ .svcName }}
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
{{- define "vaultdms.goServicePDB" -}}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: vaultdms-{{ .svcName }}
spec:
  minAvailable: 1
  selector:
    matchLabels:
      {{- include "vaultdms.selectorLabels" (dict "name" .svcName "Release" .Release) | nindent 6 }}
{{- end }}

{{/*
Generic NetworkPolicy — default deny ingress, allow only needed.
*/}}
{{- define "vaultdms.goServiceNetworkPolicy" -}}
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: vaultdms-{{ .svcName }}
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
{{- define "vaultdms.goServiceMonitor" -}}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: vaultdms-{{ .svcName }}
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ .svcName }}
  endpoints:
    - port: health
      path: /metrics
      interval: 30s
{{- end }}
