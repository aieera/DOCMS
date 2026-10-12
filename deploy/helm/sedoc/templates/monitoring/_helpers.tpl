{{/*
O-1 — monitoring values with defaults, rendered as YAML so every template
reads a complete map (`include "sedoc.monitoring.values" . | fromYaml`)
whether or not values.yaml carries a `monitoring:` block yet. The block is
documented in deploy/helm/sedoc/values-monitoring.example.yaml.
*/}}
{{- define "sedoc.monitoring.values" -}}
{{- $defaults := dict
  "enabled" false
  "prometheusRule" (dict "enabled" true "labels" (dict))
  "gatewayServiceMonitor" (dict "enabled" true "interval" "30s" "labels" (dict))
  "alertmanager" (dict
    "namespace" ""
    "configSecret" (dict "enabled" true "name" "sedoc-alertmanager-config")
    "receiversSecret" (dict
      "create" false
      "pagerdutyP0ServiceKey" ""
      "pagerdutyP1ServiceKey" ""
      "slackOncallWebhook" ""
      "slackIncidentsWebhook" "")
    "smtp" (dict "smarthost" "" "from" "" "digestTo" ""))
-}}
{{- $user := get .Values "monitoring" | default dict -}}
{{- mergeOverwrite (deepCopy $defaults) (deepCopy $user) | toYaml -}}
{{- end -}}
