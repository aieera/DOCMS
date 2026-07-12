// Package stripe handles Stripe webhook events and subscription management.
//
// Money-path contract (this is the fix):
//   - checkout.session.completed persists the Stripe customer/subscription
//     ids against the tenant (via the non-RLS stripe_customer_map, the
//     pre-tenant lookup anchor) so SeDoc knows who is subscribed;
//   - every handler returns its persistence error, and HandleWebhook
//     returns 5xx on any failure so STRIPE RETRIES — the old code
//     discarded errors and always acked 200, permanently losing failures;
//   - handlers are IDEMPOTENT by Stripe event id (stripe_processed_events
//     dedupe) so retries are safe;
//   - signature verification is preserved.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	gostripe "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"

	"github.com/aieera/sedoc/pkg/tenant"
	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// Store is the persistence surface the webhook needs. *repository.Repository
// satisfies it; tests inject a fake to drive the error/retry paths.
type Store interface {
	StripeEventAlreadyProcessed(ctx context.Context, eventID string) (bool, error)
	MarkStripeEventProcessed(ctx context.Context, eventID, eventType string) error
	UpsertCustomerMap(ctx context.Context, stripeCustomerID, stripeSubID, tenantID string) error
	TenantByStripeCustomer(ctx context.Context, stripeCustomerID string) (tenantID, subID string, found bool, err error)
	TenantByStripeSubscription(ctx context.Context, stripeSubID string) (tenantID string, found bool, err error)
	GetSubscription(ctx context.Context, tenantID string) (*model.Subscription, error)
	UpsertSubscription(ctx context.Context, sub *model.Subscription) error
	UpdateSubscriptionStatus(ctx context.Context, tenantID, status string, graceEnds *time.Time) error
}

// suspender is the tenant-routing surface (Suspend/Unsuspend on grace /
// cancel). *tenant.Router satisfies it.
type suspender interface {
	Suspend(ctx context.Context, tenantID string) error
	Unsuspend(ctx context.Context, tenantID string) error
}

// WebhookHandler processes Stripe webhook events.
type WebhookHandler struct {
	repo          Store
	router        suspender
	log           zerolog.Logger
	webhookSecret string
	graceDays     int
}

// NewWebhookHandler creates a handler. webhookSecret comes from
// cfg.StripeWebhookSecret; the caller is responsible for refusing to
// start in prod when it is empty (see billing main.go).
func NewWebhookHandler(repo *repository.Repository, rdb *redis.Client, log zerolog.Logger, webhookSecret string) *WebhookHandler {
	return &WebhookHandler{
		repo:          repo,
		router:        tenant.NewRouter(rdb),
		log:           log,
		webhookSecret: webhookSecret,
		graceDays:     7,
	}
}

// HandleWebhook is the HTTP handler for POST /internal/v1/stripe/webhook.
//
// Response contract: 400 on a bad body / bad signature; 200 on success OR
// on a duplicate redelivery; 500 when processing FAILED (so Stripe
// retries). A 200 is returned only after the event is durably persisted.
func (wh *WebhookHandler) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	var event gostripe.Event
	if wh.webhookSecret != "" {
		event, err = webhook.ConstructEvent(body, r.Header.Get("Stripe-Signature"), wh.webhookSecret)
		if err != nil {
			wh.log.Warn().Err(err).Msg("stripe signature verification failed")
			http.Error(w, "sig verify", http.StatusBadRequest)
			return
		}
	} else {
		if err := json.Unmarshal(body, &event); err != nil {
			http.Error(w, "parse", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()

	// Idempotency: a redelivery of an already-processed event is a no-op
	// ack, so retries after a successful process are safe.
	if event.ID != "" {
		done, derr := wh.repo.StripeEventAlreadyProcessed(ctx, event.ID)
		if derr != nil {
			// Can't check dedupe → don't risk double-processing or losing
			// the event; ask Stripe to retry.
			wh.log.Error().Err(derr).Str("event_id", event.ID).Msg("stripe dedupe check failed")
			http.Error(w, "dedupe check", http.StatusInternalServerError)
			return
		}
		if done {
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	if err := wh.dispatch(ctx, event); err != nil {
		// Persistence failed. Do NOT mark processed and return 5xx so
		// Stripe retries; the write is idempotent, so the retry is safe.
		wh.log.Error().Err(err).Str("type", string(event.Type)).Str("event_id", event.ID).
			Msg("stripe event processing failed; returning 500 for retry")
		http.Error(w, "processing failed", http.StatusInternalServerError)
		return
	}

	// Success → record the event id, then ack. If the mark fails, still
	// 5xx: a retry re-runs the (idempotent) processing and re-marks.
	if event.ID != "" {
		if err := wh.repo.MarkStripeEventProcessed(ctx, event.ID, string(event.Type)); err != nil {
			wh.log.Error().Err(err).Str("event_id", event.ID).Msg("mark processed failed; returning 500 for retry")
			http.Error(w, "mark processed", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// dispatch routes an event to its handler. Unhandled types are a
// success (nothing to persist) so they ack 200 and aren't retried.
func (wh *WebhookHandler) dispatch(ctx context.Context, event gostripe.Event) error {
	switch event.Type {
	case "checkout.session.completed":
		return wh.onCheckoutCompleted(ctx, event)
	case "invoice.paid":
		return wh.onInvoicePaid(ctx, event)
	case "invoice.payment_failed":
		return wh.onPaymentFailed(ctx, event)
	case "customer.subscription.updated":
		return wh.onSubscriptionUpdated(ctx, event)
	case "customer.subscription.deleted":
		return wh.onSubscriptionDeleted(ctx, event)
	default:
		wh.log.Debug().Str("type", string(event.Type)).Msg("unhandled stripe event")
		return nil
	}
}

// onCheckoutCompleted persists who-is-subscribed: the Stripe customer +
// subscription ids mapped to the tenant (carried on the session as
// client_reference_id or metadata.tenant_id), plus an active subscription
// row. This is the event the whole money path was missing.
func (wh *WebhookHandler) onCheckoutCompleted(ctx context.Context, event gostripe.Event) error {
	var sess gostripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &sess); err != nil {
		return fmt.Errorf("parse checkout session: %w", err)
	}
	tenantID := sess.ClientReferenceID
	if tenantID == "" && sess.Metadata != nil {
		tenantID = sess.Metadata["tenant_id"]
	}
	customerID := ""
	if sess.Customer != nil {
		customerID = sess.Customer.ID
	}
	subID := ""
	if sess.Subscription != nil {
		subID = sess.Subscription.ID
	}
	if tenantID == "" || customerID == "" {
		// A subscription checkout must identify its tenant + customer.
		// Missing them is a hard, visible failure (5xx → Stripe retries;
		// an operator investigates the checkout config) rather than a
		// silent drop.
		return fmt.Errorf("checkout.session.completed missing tenant_id (%q) or customer (%q)", tenantID, customerID)
	}

	// 1. The pre-tenant lookup anchor (non-RLS) so later webhooks resolve
	//    this tenant from the Stripe ids.
	if err := wh.repo.UpsertCustomerMap(ctx, customerID, subID, tenantID); err != nil {
		return fmt.Errorf("upsert customer map: %w", err)
	}

	// 2. The tenant-scoped subscription row (FORCE RLS — via withTenant).
	sub, err := wh.repo.GetSubscription(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("load subscription: %w", err)
	}
	if sub == nil {
		now := time.Now().UTC()
		sub = &model.Subscription{
			TenantID:           tenantID,
			Status:             "active",
			CurrentPeriodStart: now,
			CurrentPeriodEnd:   now.AddDate(0, 1, 0),
			CreatedAt:          now,
		}
	}
	sub.StripeCustomerID = customerID
	if subID != "" {
		sub.StripeSubID = subID
	}
	sub.Status = "active"
	if err := wh.repo.UpsertSubscription(ctx, sub); err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	wh.log.Info().Str("tenant", tenantID).Str("customer", customerID).Str("subscription", subID).
		Msg("checkout completed; subscription persisted")
	return nil
}

func (wh *WebhookHandler) onInvoicePaid(ctx context.Context, event gostripe.Event) error {
	var invoice gostripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return fmt.Errorf("parse invoice: %w", err)
	}
	tenantID, ok, err := wh.tenantForInvoice(ctx, invoice)
	if err != nil {
		return err
	}
	if !ok {
		return nil // no mapping (unknown/pre-checkout invoice) — nothing to do
	}
	if err := wh.repo.UpdateSubscriptionStatus(ctx, tenantID, "active", nil); err != nil {
		return fmt.Errorf("mark active: %w", err)
	}
	if err := wh.router.Unsuspend(ctx, tenantID); err != nil {
		return fmt.Errorf("unsuspend: %w", err)
	}
	wh.log.Info().Str("tenant", tenantID).Msg("invoice paid, subscription active")
	return nil
}

func (wh *WebhookHandler) onPaymentFailed(ctx context.Context, event gostripe.Event) error {
	var invoice gostripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return fmt.Errorf("parse invoice: %w", err)
	}
	tenantID, ok, err := wh.tenantForInvoice(ctx, invoice)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	graceEnd := time.Now().UTC().AddDate(0, 0, wh.graceDays)
	if err := wh.repo.UpdateSubscriptionStatus(ctx, tenantID, "past_due", &graceEnd); err != nil {
		return fmt.Errorf("mark past_due: %w", err)
	}
	wh.log.Warn().Str("tenant", tenantID).Time("grace_ends", graceEnd).Msg("payment failed, grace period started")
	return nil
}

func (wh *WebhookHandler) onSubscriptionUpdated(ctx context.Context, event gostripe.Event) error {
	var stripeSub gostripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &stripeSub); err != nil {
		return fmt.Errorf("parse subscription: %w", err)
	}
	tenantID, ok, err := wh.repo.TenantByStripeSubscription(ctx, stripeSub.ID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}
	if !ok {
		return nil
	}
	sub, err := wh.repo.GetSubscription(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("load subscription: %w", err)
	}
	if sub == nil {
		return nil
	}
	sub.Status = string(stripeSub.Status)
	sub.CurrentPeriodStart = time.Unix(stripeSub.CurrentPeriodStart, 0).UTC()
	sub.CurrentPeriodEnd = time.Unix(stripeSub.CurrentPeriodEnd, 0).UTC()
	if err := wh.repo.UpsertSubscription(ctx, sub); err != nil {
		return fmt.Errorf("upsert subscription: %w", err)
	}
	wh.log.Info().Str("tenant", tenantID).Str("status", sub.Status).Msg("subscription updated")
	return nil
}

func (wh *WebhookHandler) onSubscriptionDeleted(ctx context.Context, event gostripe.Event) error {
	var stripeSub gostripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &stripeSub); err != nil {
		return fmt.Errorf("parse subscription: %w", err)
	}
	tenantID, ok, err := wh.repo.TenantByStripeSubscription(ctx, stripeSub.ID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}
	if !ok {
		return nil
	}
	if err := wh.repo.UpdateSubscriptionStatus(ctx, tenantID, "cancelled", nil); err != nil {
		return fmt.Errorf("mark cancelled: %w", err)
	}
	if err := wh.router.Suspend(ctx, tenantID); err != nil {
		return fmt.Errorf("suspend: %w", err)
	}
	wh.log.Warn().Str("tenant", tenantID).Msg("subscription deleted, tenant suspended")
	return nil
}

// tenantForInvoice resolves the tenant for an invoice, preferring the
// subscription id and falling back to the customer id.
func (wh *WebhookHandler) tenantForInvoice(ctx context.Context, invoice gostripe.Invoice) (string, bool, error) {
	if invoice.Subscription != nil && invoice.Subscription.ID != "" {
		if tid, ok, err := wh.repo.TenantByStripeSubscription(ctx, invoice.Subscription.ID); err != nil {
			return "", false, fmt.Errorf("resolve tenant by subscription: %w", err)
		} else if ok {
			return tid, true, nil
		}
	}
	if invoice.Customer != nil && invoice.Customer.ID != "" {
		tid, _, ok, err := wh.repo.TenantByStripeCustomer(ctx, invoice.Customer.ID)
		if err != nil {
			return "", false, fmt.Errorf("resolve tenant by customer: %w", err)
		}
		return tid, ok, nil
	}
	return "", false, nil
}
