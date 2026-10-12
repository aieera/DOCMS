package activities

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// consumerAccepts replays the notification consumer's dms.notify.> decode
// (services/notification/internal/service/service.go StartConsumer): the
// outbox publisher wraps the payload in a CloudEvents envelope, the consumer
// reads `data` as a DeliveryPayload and Terms (drops) the message unless it
// has a tenant and at least one recipient.
func consumerAccepts(t *testing.T, outboxPayload []byte) (deliveryPayload, bool) {
	t.Helper()
	envelope, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"type":        "dms.notify.workflow_assigned.v1",
		"data":        json.RawMessage(outboxPayload),
	})
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	var payload deliveryPayload
	_ = json.Unmarshal(envelope, &payload)
	var env map[string]any
	if err := json.Unmarshal(envelope, &env); err == nil {
		if data, ok := env["data"].(map[string]any); ok {
			var inner deliveryPayload
			raw, _ := json.Marshal(data)
			if json.Unmarshal(raw, &inner) == nil && inner.TenantID != "" && len(inner.UserIDs) > 0 {
				payload = inner
			}
		}
	}
	return payload, payload.TenantID != "" && len(payload.UserIDs) > 0
}

// H-11: NotifyAssignee emitted {"user_id": …}; the consumer requires
// user_ids and Terms anything without it, so every approval, review,
// escalation and signature assignment notification was dropped and steps
// ran to their SLA timeout unseen.
func TestNotifyAssignee_ConsumerAccepts(t *testing.T) {
	tenant, assignee, doc := uuid.NewString(), uuid.NewString(), uuid.NewString()

	raw, err := assigneeNotification(tenant, assignee, doc, "Legal review")
	if err != nil {
		t.Fatalf("assigneeNotification: %v", err)
	}
	got, ok := consumerAccepts(t, raw)
	if !ok {
		t.Fatalf("the notification consumer would drop this payload: %s", raw)
	}
	want := deliveryPayload{
		TenantID:     tenant,
		UserIDs:      []string{assignee},
		Type:         "workflow.step_assigned", // the id the web notification-prefs matrix uses
		ResourceType: "document",
		ResourceID:   doc,
	}
	if got.TenantID != want.TenantID || !reflect.DeepEqual(got.UserIDs, want.UserIDs) ||
		got.Type != want.Type || got.ResourceType != want.ResourceType || got.ResourceID != want.ResourceID {
		t.Fatalf("payload = %+v, want %+v", got, want)
	}
	if got.Title == "" || !strings.Contains(got.Body, "Legal review") {
		t.Fatalf("title/body = %q / %q, want a title and the task name in the body", got.Title, got.Body)
	}
}

// The deliveryPayload mirror (activities.go) must stay identical to the consumer's declaration: same
// field names, same JSON keys. Parsed from source because the type is
// internal to the notification module.
func TestDeliveryPayloadMirror_MatchesNotificationModel(t *testing.T) {
	const src = "../../../notification/internal/model/model.go"
	f, err := parser.ParseFile(token.NewFileSet(), src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}
	real := map[string]string{} // field name → json tag
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "DeliveryPayload" {
			return true
		}
		for _, fld := range ts.Type.(*ast.StructType).Fields.List {
			if fld.Tag == nil || len(fld.Names) == 0 {
				continue
			}
			tag, _ := strconv.Unquote(fld.Tag.Value)
			real[fld.Names[0].Name] = reflect.StructTag(tag).Get("json")
		}
		return false
	})
	if len(real) == 0 {
		t.Fatalf("DeliveryPayload not found in %s", src)
	}
	mirror := reflect.TypeOf(deliveryPayload{})
	for i := 0; i < mirror.NumField(); i++ {
		f := mirror.Field(i)
		tag, ok := real[f.Name]
		if !ok {
			t.Errorf("field %s is not in notification's DeliveryPayload", f.Name)
			continue
		}
		if got := f.Tag.Get("json"); got != tag {
			t.Errorf("field %s: mirror json tag %q, notification has %q", f.Name, got, tag)
		}
	}
}
