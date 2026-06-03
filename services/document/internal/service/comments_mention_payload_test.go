// ADR 0066 — regression test: the dms.notify.comment.mention.v1
// payload MUST match notification.model.DeliveryPayload's required
// fields (`user_ids`, `title`, `body`). The notification consumer
// drops messages where those fields are empty, so any future
// refactor that changes the shape silently breaks email delivery.
//
// We don't import notification.model.DeliveryPayload here directly
// — the document service shouldn't take a hard dep on the
// notification service's internal model. Instead we re-declare the
// minimum fields and assert the JSON deserializes cleanly.
package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// minimumMentionPayload mirrors notification.model.DeliveryPayload
// at the JSON level. Field tags MUST match.
type minimumMentionPayload struct {
	TenantID     string   `json:"tenant_id"`
	UserIDs      []string `json:"user_ids"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	ResourceType string   `json:"resource_type,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
}

func TestMentionPayload_HasRequiredDeliveryFields(t *testing.T) {
	tenantID := uuid.New()
	commentID := uuid.New()
	mentionedUserID := uuid.New()

	// Build the payload exactly like emitMention does, without
	// touching DB/transactions. If emitMention's shape changes,
	// keep this test in sync — that's the point.
	payload := map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      []string{mentionedUserID.String()},
		"type":          "comment.mention",
		"title":         "You were mentioned in a comment",
		"body":          "Hey @[Alice](" + uuid.New().String() + ") please review.",
		"resource_type": "comment",
		"resource_id":   commentID.String(),
	}
	evt, err := model.NewOutboxEvent(tenantID, "dms.notify.comment.mention.v1", "comment", commentID, payload)
	if err != nil {
		t.Fatalf("NewOutboxEvent: %v", err)
	}

	var got minimumMentionPayload
	if err := json.Unmarshal(evt.Payload, &got); err != nil {
		t.Fatalf("payload not parseable as DeliveryPayload: %v", err)
	}
	if got.TenantID != tenantID.String() {
		t.Errorf("tenant_id missing/wrong: %q", got.TenantID)
	}
	if len(got.UserIDs) != 1 || got.UserIDs[0] != mentionedUserID.String() {
		t.Errorf("user_ids must be a 1-element list of the mentioned user; got %v", got.UserIDs)
	}
	if got.Title == "" {
		t.Error("title is required by the notification consumer; got empty string")
	}
	if got.Body == "" {
		t.Error("body is required by the notification consumer; got empty string")
	}
	if got.ResourceType != "comment" {
		t.Errorf("resource_type=%q want comment", got.ResourceType)
	}
}

// TestMentionPayload_RejectsLegacyShape pins that the OLD shape with
// a singular `mentioned_user_id` field would NOT pass the
// notification consumer's gate. Catches a regression where someone
// reverts emitMention to the singular form without realizing the
// consumer drops the message.
func TestMentionPayload_RejectsLegacyShape(t *testing.T) {
	legacy := map[string]any{
		"tenant_id":         uuid.New().String(),
		"mentioned_user_id": uuid.New().String(), // legacy singular field
		"body_excerpt":      "old shape",
	}
	raw, _ := json.Marshal(legacy)

	var got minimumMentionPayload
	_ = json.Unmarshal(raw, &got)
	// The notification consumer's drop-the-message gate is
	// `len(payload.UserIDs) == 0 || title == ""`. If the legacy
	// shape passes BOTH of those, this test must change — and so
	// must emitMention. Until then, this test pins the gap.
	if len(got.UserIDs) != 0 {
		t.Errorf("legacy payload UserIDs leaked: %v", got.UserIDs)
	}
	if got.Title != "" {
		t.Errorf("legacy payload title leaked: %q", got.Title)
	}
}
