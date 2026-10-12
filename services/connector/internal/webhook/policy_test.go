package webhook

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/connector/internal/model"
)

// fakeDeliveryStore serves one active subscription pointing at the test
// receiver and records every delivery update.
type fakeDeliveryStore struct {
	url     string
	updates []model.WebhookDelivery
}

func (f *fakeDeliveryStore) GetWebhook(_ context.Context, tenantID, id string) (*model.WebhookSubscription, error) {
	return &model.WebhookSubscription{ID: id, TenantID: tenantID, URL: f.url, Secret: "whsec_x", Active: true}, nil
}

func (f *fakeDeliveryStore) FinishDelivery(_ context.Context, d *model.WebhookDelivery, _ int) (bool, error) {
	f.updates = append(f.updates, *d)
	return true, nil
}

func (f *fakeDeliveryStore) ClaimPendingDeliveries(context.Context, int, time.Duration) ([]*model.WebhookDelivery, error) {
	return nil, nil
}

// TestDeliver_DeadLettersQueuedAuthEvents: a delivery row queued before the
// DEC-16 allow-list (e.g. a password-reset event carrying the reset token)
// must be dead-lettered without ever reaching the receiver.
func TestDeliver_DeadLettersQueuedAuthEvents(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeDeliveryStore{url: srv.URL}
	w := &DeliveryWorker{store: store, log: zerolog.Nop(), hc: newSSRFSafeClient(5e9, true)}
	w.deliver(context.Background(), &model.WebhookDelivery{
		ID: "d1", SubscriptionID: "s1", TenantID: "t1",
		EventType: "dms.auth.password_reset_requested.v1",
		Payload:   []byte(`{"data":{"token":"plaintext"}}`),
	})
	if hits.Load() != 0 {
		t.Fatalf("auth event reached the receiver %d time(s)", hits.Load())
	}
	if len(store.updates) != 1 || !store.updates[0].DeadLettered {
		t.Fatalf("want one dead-letter update, got %+v", store.updates)
	}

	// A subscribable event still goes out.
	w.deliver(context.Background(), &model.WebhookDelivery{
		ID: "d2", SubscriptionID: "s1", TenantID: "t1",
		EventType: "dms.document.created.v1", Payload: []byte(`{}`),
	})
	if hits.Load() != 1 {
		t.Fatalf("document event should be delivered once, hits=%d", hits.Load())
	}
}
