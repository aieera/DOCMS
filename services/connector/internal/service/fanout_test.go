package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
)

// fakeFanoutStore records every lookup and insert the fan-out performs. It
// returns a matching active subscription for ANY event type, which models a
// legacy subscription created before the allow-list existed (e.g. one that
// subscribed to dms.auth.password_reset_requested.v1).
type fakeFanoutStore struct {
	lookups []string
	inserts []*model.WebhookDelivery
}

func (f *fakeFanoutStore) GetActiveWebhooksForEvent(_ context.Context, tenantID, eventType string) ([]*model.WebhookSubscription, error) {
	f.lookups = append(f.lookups, eventType)
	return []*model.WebhookSubscription{{ID: "sub-1", TenantID: tenantID, Events: []string{eventType}, Active: true}}, nil
}

func (f *fakeFanoutStore) InsertDelivery(_ context.Context, d *model.WebhookDelivery) error {
	f.inserts = append(f.inserts, d)
	return nil
}

const fanoutTenant = "6f1c1b7e-8a8e-4a43-9a1e-0f7a3f9d2c11"

func cloudEvent(t *testing.T, eventType string, data map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0",
		"id":          "evt-1",
		"type":        eventType,
		"tenantid":    fanoutTenant,
		"data":        data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFanout_NeverDeliversAuthSubjects(t *testing.T) {
	cases := []struct {
		name, subject, eventType string
	}{
		{"password reset carries token", "dms.auth.password_reset_requested.v1", "dms.auth.password_reset_requested.v1"},
		{"login", "dms.auth.login_success.v1", "dms.auth.login_success.v1"},
		{"session", "dms.session.created.v1", "dms.session.created.v1"},
		{"api key", "dms.apikey.rotated.v1", "dms.apikey.rotated.v1"},
		{"user", "dms.user.invited.v1", "dms.user.invited.v1"},
		{"audit", "dms.audit.event.v1", "dms.audit.event.v1"},
		// A message on an allowed subject whose envelope claims an auth type
		// is still dropped: both must pass.
		{"auth type on document subject", "dms.document.created.v1", "dms.auth.password_reset_requested.v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeFanoutStore{}
			s := &Service{log: zerolog.Nop()}
			data := cloudEvent(t, tc.eventType, map[string]any{"token": "plaintext-reset-token", "tenant_id": fanoutTenant})
			s.fanoutMessage(context.Background(), store, tc.subject, data)
			if len(store.inserts) != 0 {
				t.Fatalf("fan-out queued %d deliveries for %s/%s; must queue none", len(store.inserts), tc.subject, tc.eventType)
			}
			if len(store.lookups) != 0 {
				t.Fatalf("fan-out looked up subscriptions for %v; must drop before lookup", store.lookups)
			}
		})
	}
}

func TestFanout_DeliversSubscribableEvent(t *testing.T) {
	store := &fakeFanoutStore{}
	s := &Service{log: zerolog.Nop()}
	data := cloudEvent(t, "dms.document.created.v1", map[string]any{"document_id": "d1"})
	s.fanoutMessage(context.Background(), store, "dms.document.created.v1", data)
	if len(store.inserts) != 1 {
		t.Fatalf("want 1 delivery, got %d", len(store.inserts))
	}
	d := store.inserts[0]
	if d.EventType != "dms.document.created.v1" || d.TenantID != fanoutTenant || d.SubscriptionID != "sub-1" {
		t.Fatalf("unexpected delivery %+v", d)
	}
}

func TestCreateWebhook_RejectsAuthEvents(t *testing.T) {
	s := New(Config{Logger: zerolog.Nop()})
	for _, ev := range []string{
		"dms.auth.password_reset_requested.v1",
		"dms.session.created.v1",
		"dms.apikey.issued.v1",
		"dms.user.invited.v1",
		"dms.audit.event.v1",
	} {
		// The allowed event rides along to prove one bad entry rejects the
		// whole request rather than being silently filtered.
		_, err := s.CreateWebhook(context.Background(), fanoutTenant, "u1",
			"https://receiver.example.com/hook", []string{"dms.document.created.v1", ev})
		if err == nil {
			t.Fatalf("CreateWebhook accepted non-subscribable event %q", ev)
		}
		if !strings.Contains(err.Error(), ev) {
			t.Errorf("error should name the rejected event %q: %v", ev, err)
		}
	}
}
