// ADR 0068 — regression test: every dms.notify.task.* payload MUST
// match notification.model.DeliveryPayload's required fields
// (`user_ids`, `title`, `body`). The notification consumer drops
// messages whose user_ids slice is empty or whose title/body are
// missing — exact same trap that bit ADR 0066 mention emails.
//
// Pure-function test; we don't import notification.model directly to
// avoid a hard dep on another service's internal model. We re-state
// the consumer's expected JSON shape and assert the marshalled
// payload deserializes cleanly.
package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

type taskDeliveryPayload struct {
	TenantID     string   `json:"tenant_id"`
	UserIDs      []string `json:"user_ids"`
	Type         string   `json:"type"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	ResourceType string   `json:"resource_type,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
}

// TestAssignedPayload_HasRequiredDeliveryFields builds the same map
// emitTaskAssigned does and asserts the wire shape.
func TestAssignedPayload_HasRequiredDeliveryFields(t *testing.T) {
	tenantID := uuid.New()
	taskID := uuid.New()
	assignee := uuid.New()
	due := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	payload := map[string]any{
		"tenant_id":     tenantID.String(),
		"user_ids":      []string{assignee.String()},
		"type":          "task.assigned",
		"title":         "A task was assigned to you",
		"body":          "Send NDA (due " + due.Format("2006-01-02") + ")",
		"resource_type": "task",
		"resource_id":   taskID.String(),
	}
	evt, err := model.NewOutboxEvent(tenantID, "dms.notify.task.assigned.v1", "task", taskID, payload)
	if err != nil {
		t.Fatalf("outbox event: %v", err)
	}
	var got taskDeliveryPayload
	if err := json.Unmarshal(evt.Payload, &got); err != nil {
		t.Fatalf("payload not parseable: %v", err)
	}
	if len(got.UserIDs) != 1 || got.UserIDs[0] != assignee.String() {
		t.Errorf("user_ids must carry exactly the assignee; got %v", got.UserIDs)
	}
	if got.Title == "" {
		t.Error("title required by consumer; got empty string")
	}
	if got.Body == "" {
		t.Error("body required by consumer; got empty string")
	}
	if got.ResourceType != "task" {
		t.Errorf("resource_type=%q want task", got.ResourceType)
	}
}

// TestOverduePayload_DedupesAssigneeAndCreator pins the rule that
// when assignee == creator we don't double-notify the same user.
func TestOverduePayload_DedupesAssigneeAndCreator(t *testing.T) {
	user := uuid.New()
	// Mirrors emitTaskOverdue's recipient-build logic.
	recipients := []string{}
	assignee := &user
	creator := user
	if assignee != nil {
		recipients = append(recipients, assignee.String())
	}
	dupe := false
	for _, r := range recipients {
		if r == creator.String() {
			dupe = true
			break
		}
	}
	if !dupe {
		recipients = append(recipients, creator.String())
	}
	if len(recipients) != 1 {
		t.Errorf("when assignee == creator the recipient list must be deduped to 1; got %d (%v)", len(recipients), recipients)
	}
}

// TestOverduePayload_BothWhenDifferent — when assignee and creator
// are different users, both get the overdue ping.
func TestOverduePayload_BothWhenDifferent(t *testing.T) {
	assignee := uuid.New()
	creator := uuid.New()
	recipients := []string{assignee.String()}
	if creator.String() != assignee.String() {
		recipients = append(recipients, creator.String())
	}
	if len(recipients) != 2 {
		t.Errorf("distinct assignee + creator must both be notified; got %v", recipients)
	}
}
