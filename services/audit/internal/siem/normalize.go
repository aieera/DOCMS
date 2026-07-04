package siem

import (
	"encoding/json"
	"time"
)

// Normalize maps a raw domain event (the NATS subject + the CloudEvents-ish
// JSON body) to the common NormalizedEvent shape. Tolerant of both a flat
// payload and a {data:{...}} envelope; unknown fields are preserved in Raw.
// Deterministic + dependency-free so it is unit-testable.
func Normalize(subject string, raw []byte, receivedAt time.Time) NormalizedEvent {
	ev := NormalizedEvent{
		Subject:   subject,
		Action:    subject,
		Timestamp: receivedAt.UTC().Format(time.RFC3339Nano),
		Raw:       json.RawMessage(raw),
	}

	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return ev // non-JSON body: keep subject + raw, best effort
	}

	// Fields may live at the top level or inside a `data` envelope.
	data := top
	if d, ok := top["data"].(map[string]any); ok {
		data = d
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := data[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
			if v, ok := top[k]; ok {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		return ""
	}

	ev.TenantID = pick("tenant_id", "tenantId")
	if a := pick("action", "event_type", "type"); a != "" {
		ev.Action = a
	}
	ev.Actor = pick("actor", "actor_id", "user_id", "uploaded_by_user_id")
	ev.ActorName = pick("actor_name", "actorName")
	ev.ResourceType = pick("resource_type", "aggregate_type")
	ev.ResourceID = pick("resource_id", "aggregate_id", "document_id", "record_id")
	ev.EventHash = pick("event_hash", "eventHash")
	ev.CorrelationID = pick("correlation_id", "correlationId")
	if t := pick("time", "created_at", "occurred_at", "@timestamp"); t != "" {
		ev.Timestamp = t
	}
	return ev
}
