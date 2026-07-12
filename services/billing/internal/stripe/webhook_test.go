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
	"github.com/stretchr/testify/require"
	"github.com/stripe/stripe-go/v76/webhook"

	"github.com/aieera/sedoc/services/billing/internal/model"
)

const testWHSecret = "whsec_testsecret"

// fakeStore is an in-memory Store; failUpsertSub flips on to simulate a
// persistence failure (the retry-ability test), then off for the retry.
type fakeStore struct {
	mu              sync.Mutex
	subs            map[string]*model.Subscription // tenant → sub
	custMap         map[string][2]string           // customer → {sub, tenant}
	subToTenant     map[string]string              // sub → tenant
	processed       map[string]bool                // event id → done
	failUpsertSub   bool
	failDedupeCheck bool // StripeEventAlreadyProcessed returns an error
	failMark        bool // MarkStripeEventProcessed returns an error
	upsertSubHits   int
	statusUpdates   []statusUpdate // ordered log of UpdateSubscriptionStatus calls
}

type statusUpdate struct {
	tenant string
	status string
	grace  *time.Time
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
	if f.failDedupeCheck {
		return false, errors.New("injected: dedupe check failed")
	}
	return f.processed[id], nil
}
func (f *fakeStore) MarkStripeEventProcessed(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMark {
		return errors.New("injected: mark processed failed")
	}
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
	f.statusUpdates = append(f.statusUpdates, statusUpdate{tenant: tenant, status: status, grace: grace})
	if s := f.subs[tenant]; s != nil {
		s.Status = status
		s.GracePeriodEnds = grace
	}
	return nil
}

type noopSuspender struct{}

func (noopSuspender) Suspend(context.Context, string) error   { return nil }
func (noopSuspender) Unsuspend(context.Context, string) error { return nil }

// spySuspender records Suspend/Unsuspend calls so tests can assert the
// tenant-routing side effects of the money-path events.
type spySuspender struct {
	mu         sync.Mutex
	suspends   []string
	unsuspends []string
}

func (s *spySuspender) Suspend(_ context.Context, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspends = append(s.suspends, tenant)
	return nil
}
func (s *spySuspender) Unsuspend(_ context.Context, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unsuspends = append(s.unsuspends, tenant)
	return nil
}

func newTestWH(store Store) *WebhookHandler {
	return newTestWHWith(store, noopSuspender{})
}

func newTestWHWith(store Store, susp suspender) *WebhookHandler {
	return &WebhookHandler{
		repo: store, router: susp, log: zerolog.Nop(),
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

func invoicePayload(eventID, eventType, customer, sub string) string {
	return fmt.Sprintf(`{"id":%q,"type":%q,"api_version":"2023-10-16","data":{"object":{
		"id":"in_1","object":"invoice",
		"customer":{"id":%q,"object":"customer"},
		"subscription":{"id":%q,"object":"subscription"}}}}`, eventID, eventType, customer, sub)
}

func subEventPayload(eventID, eventType, subID, status string) string {
	return fmt.Sprintf(`{"id":%q,"type":%q,"api_version":"2023-10-16","data":{"object":{
		"id":%q,"object":"subscription","status":%q,
		"current_period_start":1,"current_period_end":2}}}`, eventID, eventType, subID, status)
}

// invoice.paid must reactivate the subscription AND lift any suspension.
func TestWebhook_InvoicePaid_ActivatesAndUnsuspends(t *testing.T) {
	store := newFakeStore()
	spy := &spySuspender{}
	wh := newTestWHWith(store, spy)

	// checkout → active + mapping; then a failed payment → past_due.
	require.Equal(t, 200, post(t, wh, checkoutPayload("evt_c", "tenant-1", "cus_1", "sub_1"), "").Code)
	require.Equal(t, 200, post(t, wh, invoicePayload("evt_fail", "invoice.payment_failed", "cus_1", "sub_1"), "").Code)
	require.Equal(t, "past_due", store.subs["tenant-1"].Status)

	// invoice.paid → back to active, tenant unsuspended.
	rec := post(t, wh, invoicePayload("evt_paid", "invoice.paid", "cus_1", "sub_1"), "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Equal(t, "active", store.subs["tenant-1"].Status)
	require.Nil(t, store.subs["tenant-1"].GracePeriodEnds, "paying clears the grace deadline")
	require.Contains(t, spy.unsuspends, "tenant-1", "invoice.paid must unsuspend the tenant")
}

// invoice.payment_failed must move the sub to past_due with a grace deadline.
func TestWebhook_PaymentFailed_StartsGracePeriod(t *testing.T) {
	store := newFakeStore()
	wh := newTestWHWith(store, &spySuspender{})
	require.Equal(t, 200, post(t, wh, checkoutPayload("evt_c", "tenant-2", "cus_2", "sub_2"), "").Code)

	before := time.Now().UTC()
	rec := post(t, wh, invoicePayload("evt_fail", "invoice.payment_failed", "cus_2", "sub_2"), "")
	require.Equal(t, 200, rec.Code, rec.Body.String())

	sub := store.subs["tenant-2"]
	require.Equal(t, "past_due", sub.Status)
	require.NotNil(t, sub.GracePeriodEnds, "a failed payment must set a grace deadline")
	// graceDays = 7; the deadline is ~7 days out.
	wantMin := before.AddDate(0, 0, 7).Add(-time.Minute)
	require.True(t, sub.GracePeriodEnds.After(wantMin),
		"grace deadline %s should be ~7 days out", sub.GracePeriodEnds)
}

// An event type the switch doesn't handle is a success ack (200), persists
// nothing, and is recorded processed so Stripe won't retry it forever.
func TestWebhook_UnhandledEventType_AckedWithoutSideEffects(t *testing.T) {
	store := newFakeStore()
	wh := newTestWH(store)
	payload := `{"id":"evt_unh","type":"customer.updated","api_version":"2023-10-16","data":{"object":{"id":"cus_x","object":"customer"}}}`
	rec := post(t, wh, payload, "")
	require.Equal(t, 200, rec.Code)
	require.Empty(t, store.subs, "unhandled event must persist nothing")
	require.True(t, store.processed["evt_unh"], "unhandled event is acked processed (not retried)")
}

// A subscription checkout that can't be attributed to a tenant+customer is a
// hard 5xx (operator-visible + Stripe-retried), never a silent drop.
func TestWebhook_CheckoutMissingAttribution_5xx(t *testing.T) {
	tests := []struct {
		name             string
		tenant, customer string
	}{
		{"missing tenant", "", "cus_1"},
		{"missing customer", "tenant-1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			wh := newTestWH(store)
			rec := post(t, wh, checkoutPayload("evt_bad", tc.tenant, tc.customer, "sub_1"), "")
			require.Equal(t, http.StatusInternalServerError, rec.Code)
			require.False(t, store.processed["evt_bad"], "a failed checkout must not be marked processed")
			require.Empty(t, store.subs)
		})
	}
}

// If the dedupe check itself errors, the handler must 5xx (retry) rather than
// risk double-processing or losing the event.
func TestWebhook_DedupeCheckError_5xx(t *testing.T) {
	store := newFakeStore()
	store.failDedupeCheck = true
	wh := newTestWH(store)
	rec := post(t, wh, checkoutPayload("evt_1", "tenant-1", "cus_1", "sub_1"), "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Zero(t, store.upsertSubHits, "dedupe failure must abort before any processing")
}

// If processing SUCCEEDS but recording the event id fails, the handler must
// 5xx so Stripe retries — the write is idempotent so the retry is safe.
func TestWebhook_MarkProcessedError_5xx(t *testing.T) {
	store := newFakeStore()
	store.failMark = true
	wh := newTestWH(store)
	rec := post(t, wh, checkoutPayload("evt_1", "tenant-1", "cus_1", "sub_1"), "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.NotNil(t, store.subs["tenant-1"], "processing did happen (retry re-marks idempotently)")
	require.False(t, store.processed["evt_1"], "the event id must not be recorded when the mark failed")
}

// The full money-path lifecycle: trial → paid → canceled → resubscribed.
func TestWebhook_Lifecycle_TrialToPaidToCanceledToResubscribed(t *testing.T) {
	store := newFakeStore()
	spy := &spySuspender{}
	wh := newTestWHWith(store, spy)
	const tenant = "tenant-lc"

	// Subscribe: checkout wires the stripe↔tenant mapping + an active sub.
	require.Equal(t, 200, post(t, wh, checkoutPayload("evt_sub", tenant, "cus_lc", "sub_lc"), "").Code)

	// Trial: Stripe reports the subscription as trialing.
	require.Equal(t, 200, post(t, wh, subEventPayload("evt_trial", "customer.subscription.updated", "sub_lc", "trialing"), "").Code)
	require.Equal(t, "trialing", store.subs[tenant].Status)

	// Paid: first invoice settles → active + (idempotent) unsuspend.
	require.Equal(t, 200, post(t, wh, invoicePayload("evt_paid", "invoice.paid", "cus_lc", "sub_lc"), "").Code)
	require.Equal(t, "active", store.subs[tenant].Status)

	// Canceled: subscription deleted → cancelled + tenant suspended.
	require.Equal(t, 200, post(t, wh, subEventPayload("evt_cancel", "customer.subscription.deleted", "sub_lc", "canceled"), "").Code)
	require.Equal(t, "cancelled", store.subs[tenant].Status)
	require.Contains(t, spy.suspends, tenant, "cancellation must suspend the tenant")

	// Resubscribed: a fresh checkout re-activates the same tenant with a new
	// Stripe subscription id, re-mapped for future events.
	require.Equal(t, 200, post(t, wh, checkoutPayload("evt_resub", tenant, "cus_lc", "sub_lc2"), "").Code)
	require.Equal(t, "active", store.subs[tenant].Status, "resubscribe must reactivate")
	require.Equal(t, tenant, store.subToTenant["sub_lc2"], "the new subscription id must be re-mapped to the tenant")
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
