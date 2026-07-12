package stripe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stripe/stripe-go/v76/webhook"

	"github.com/aieera/sedoc/services/billing/internal/model"
)

const testWHSecret = "whsec_testsecret"

// fakeStore is an in-memory Store; failUpsertSub flips on to simulate a
// persistence failure (the retry-ability test), then off for the retry.
type fakeStore struct {
	mu            sync.Mutex
	subs          map[string]*model.Subscription // tenant → sub
	custMap       map[string][2]string           // customer → {sub, tenant}
	subToTenant   map[string]string              // sub → tenant
	processed     map[string]bool                // event id → done
	failUpsertSub bool
	upsertSubHits int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		subs: map[string]*model.Subscription{}, custMap: map[string][2]string{},
		subToTenant: map[string]string{}, processed: map[string]bool{},
	}
}

func (f *fakeStore) StripeEventAlreadyProcessed(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.processed[id], nil
}
func (f *fakeStore) MarkStripeEventProcessed(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.processed[id] = true
	return nil
}
func (f *fakeStore) UpsertCustomerMap(_ context.Context, cust, sub, tenant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.custMap[cust] = [2]string{sub, tenant}
	if sub != "" {
		f.subToTenant[sub] = tenant
	}
	return nil
}
func (f *fakeStore) TenantByStripeCustomer(_ context.Context, cust string) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.custMap[cust]
	if !ok {
		return "", "", false, nil
	}
	return v[1], v[0], true, nil
}
func (f *fakeStore) TenantByStripeSubscription(_ context.Context, sub string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.subToTenant[sub]
	return t, ok, nil
}
func (f *fakeStore) GetSubscription(_ context.Context, tenant string) (*model.Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs[tenant], nil
}
func (f *fakeStore) UpsertSubscription(_ context.Context, sub *model.Subscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertSubHits++
	if f.failUpsertSub {
		return errors.New("injected: subscriptions write failed")
	}
	cp := *sub
	f.subs[sub.TenantID] = &cp
	return nil
}
func (f *fakeStore) UpdateSubscriptionStatus(_ context.Context, tenant, status string, grace *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s := f.subs[tenant]; s != nil {
		s.Status = status
		s.GracePeriodEnds = grace
	}
	return nil
}

type noopSuspender struct{}

func (noopSuspender) Suspend(context.Context, string) error   { return nil }
func (noopSuspender) Unsuspend(context.Context, string) error { return nil }

func newTestWH(store Store) *WebhookHandler {
	return &WebhookHandler{
		repo: store, router: noopSuspender{}, log: zerolog.Nop(),
		webhookSecret: testWHSecret, graceDays: 7,
	}
}

// post signs the payload with the test secret (unless raw!=nil overrides
// the signature) and runs it through the handler.
func post(t *testing.T, wh *WebhookHandler, payload string, sigOverride string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/stripe/webhook", strings.NewReader(payload))
	if sigOverride != "" {
		req.Header.Set("Stripe-Signature", sigOverride)
	} else {
		now := time.Now()
		sig := webhook.ComputeSignature(now, []byte(payload), testWHSecret)
		req.Header.Set("Stripe-Signature", fmt.Sprintf("t=%d,v1=%x", now.Unix(), sig))
	}
	rec := httptest.NewRecorder()
	wh.HandleWebhook(rec, req)
	return rec
}

func checkoutPayload(eventID, tenant, customer, sub string) string {
	return fmt.Sprintf(`{
		"id": %q, "type": "checkout.session.completed", "api_version": "2023-10-16",
		"data": {"object": {
			"id": "cs_test_1", "object": "checkout.session",
			"client_reference_id": %q,
			"customer": {"id": %q, "object": "customer"},
			"subscription": {"id": %q, "object": "subscription"}
		}}
	}`, eventID, tenant, customer, sub)
}

func TestWebhook_SignedCheckoutPersistsSubscription(t *testing.T) {
	store := newFakeStore()
	wh := newTestWH(store)

	rec := post(t, wh, checkoutPayload("evt_1", "tenant-1", "cus_1", "sub_1"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200: %s", rec.Code, rec.Body.String())
	}
	// The subscription row carries the right ids, active.
	sub := store.subs["tenant-1"]
	if sub == nil {
		t.Fatal("no subscription row persisted")
	}
	if sub.StripeCustomerID != "cus_1" || sub.StripeSubID != "sub_1" || sub.Status != "active" {
		t.Fatalf("bad subscription persisted: %+v", sub)
	}
	// The stripe→tenant map was written so later webhooks resolve it.
	if store.custMap["cus_1"] != [2]string{"sub_1", "tenant-1"} {
		t.Fatalf("customer map not written: %+v", store.custMap["cus_1"])
	}
	if store.subToTenant["sub_1"] != "tenant-1" {
		t.Fatal("subscription→tenant mapping missing")
	}
}

func TestWebhook_UnsignedRejected(t *testing.T) {
	wh := newTestWH(newFakeStore())
	// A syntactically-plausible but wrong signature must be rejected 400
	// and nothing processed.
	rec := post(t, wh, checkoutPayload("evt_x", "tenant-1", "cus_1", "sub_1"),
		"t=123,v1=deadbeef")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d want 400 for bad signature", rec.Code)
	}
}

func TestWebhook_PersistenceFailureReturns5xxThenRetrySucceedsIdempotently(t *testing.T) {
	store := newFakeStore()
	store.failUpsertSub = true
	wh := newTestWH(store)
	payload := checkoutPayload("evt_retry", "tenant-1", "cus_1", "sub_1")

	// First delivery: the subscription write fails → 500 (Stripe retries),
	// and the event must NOT be marked processed.
	rec := post(t, wh, payload, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code=%d want 500 on persistence failure", rec.Code)
	}
	if store.processed["evt_retry"] {
		t.Fatal("a failed event must NOT be marked processed (else the retry is skipped)")
	}

	// Retry (same event id): persistence now succeeds → 200, processed.
	store.failUpsertSub = false
	rec = post(t, wh, payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry code=%d want 200: %s", rec.Code, rec.Body.String())
	}
	if store.subs["tenant-1"] == nil {
		t.Fatal("retry must persist the subscription")
	}
	if !store.processed["evt_retry"] {
		t.Fatal("successful retry must mark the event processed")
	}

	// A THIRD delivery of the same id is a no-op ack (idempotent) — the
	// subscription write is not run again.
	before := store.upsertSubHits
	rec = post(t, wh, payload, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate code=%d want 200", rec.Code)
	}
	if store.upsertSubHits != before {
		t.Fatalf("duplicate event re-processed (upsert hits %d→%d)", before, store.upsertSubHits)
	}
}

func TestWebhook_LifecycleUpdateAndCancel(t *testing.T) {
	store := newFakeStore()
	wh := newTestWH(store)

	// checkout → creates the mapping + active sub.
	if rec := post(t, wh, checkoutPayload("evt_c", "tenant-9", "cus_9", "sub_9"), ""); rec.Code != 200 {
		t.Fatalf("checkout: %d", rec.Code)
	}
	// subscription.updated → status reflected.
	upd := `{"id":"evt_u","type":"customer.subscription.updated","api_version":"2023-10-16","data":{"object":{"id":"sub_9","object":"subscription","status":"past_due","current_period_start":1,"current_period_end":2}}}`
	if rec := post(t, wh, upd, ""); rec.Code != 200 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if store.subs["tenant-9"].Status != "past_due" {
		t.Fatalf("update not reflected: %+v", store.subs["tenant-9"])
	}
	// subscription.deleted → cancelled.
	del := `{"id":"evt_d","type":"customer.subscription.deleted","api_version":"2023-10-16","data":{"object":{"id":"sub_9","object":"subscription","status":"canceled"}}}`
	if rec := post(t, wh, del, ""); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if store.subs["tenant-9"].Status != "cancelled" {
		t.Fatalf("cancel not reflected: %+v", store.subs["tenant-9"])
	}
}
