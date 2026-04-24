// Package stripe handles Stripe webhook events and subscription management.
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

	"github.com/vaultdms/vaultdms/pkg/tenant"
	"github.com/vaultdms/vaultdms/services/billing/internal/repository"
)

// WebhookHandler processes Stripe webhook events.
type WebhookHandler struct {
	repo          *repository.Repository
	router        *tenant.Router
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

	// Idempotency guard. Stripe retries up to 3 days; handlers must
	// be re-entrant. INSERT-ON-CONFLICT returns false on duplicate →
	// we 200 OK without re-running the handler so Stripe stops
	// retrying but we don't double-apply side effects.
	isNew, err := wh.repo.InsertStripeEvent(ctx, event.ID, string(event.Type), body)
	if err != nil {
		wh.log.Error().Err(err).Str("event_id", event.ID).Msg("stripe_events insert")
		// Fail the webhook so Stripe retries once the DB recovers.
		http.Error(w, "persist", http.StatusServiceUnavailable)
		return
	}
	if !isNew {
		wh.log.Debug().Str("event_id", event.ID).Msg("stripe duplicate — skipping")
		w.WriteHeader(http.StatusOK)
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		wh.onCheckoutCompleted(ctx, event)
	case "invoice.paid":
		wh.onInvoicePaid(ctx, event)
	case "invoice.payment_failed":
		wh.onPaymentFailed(ctx, event)
	case "customer.subscription.updated":
		wh.onSubscriptionUpdated(ctx, event)
	case "customer.subscription.deleted":
		wh.onSubscriptionDeleted(ctx, event)
	default:
		wh.log.Debug().Str("type", string(event.Type)).Msg("unhandled stripe event")
	}

	w.WriteHeader(http.StatusOK)
}

// onCheckoutCompleted links a freshly-paid Stripe checkout to the
// VaultDMS tenant it provisioned. Contract with the frontend:
// client_reference_id MUST be the provisioning-intent id the
// checkout was opened with (that id is also the future tenant_id).
// metadata.tenant_id wins over client_reference_id when both set —
// lets the operator attach legacy Stripe sessions to existing tenants.
func (wh *WebhookHandler) onCheckoutCompleted(ctx context.Context, event gostripe.Event) {
	var session gostripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		wh.log.Warn().Err(err).Msg("parse checkout session")
		return
	}
	tenantID := session.ClientReferenceID
	if session.Metadata != nil {
		if v := session.Metadata["tenant_id"]; v != "" {
			tenantID = v
		}
	}
	if tenantID == "" {
		wh.log.Warn().Str("session", session.ID).Msg("checkout.session.completed with no tenant reference")
		return
	}
	// Activate the subscription + clear any grace/suspended marker.
	_ = wh.repo.UpdateSubscriptionStatus(ctx, tenantID, "active", nil)
	_ = wh.router.Unsuspend(ctx, tenantID)
	wh.log.Info().Str("tenant", tenantID).Str("session", session.ID).Msg("checkout completed; tenant active")
}

func (wh *WebhookHandler) onInvoicePaid(ctx context.Context, event gostripe.Event) {
	var invoice gostripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return
	}
	subID := ""
	if invoice.Subscription != nil {
		subID = invoice.Subscription.ID
	}
	if subID == "" {
		return
	}
	sub, err := wh.repo.GetSubscriptionByStripeID(ctx, subID)
	if err != nil || sub == nil {
		return
	}
	// Clear grace period, mark active.
	_ = wh.repo.UpdateSubscriptionStatus(ctx, sub.TenantID, "active", nil)
	_ = wh.router.Unsuspend(ctx, sub.TenantID)
	wh.log.Info().Str("tenant", sub.TenantID).Msg("invoice paid, subscription active")
}

func (wh *WebhookHandler) onPaymentFailed(ctx context.Context, event gostripe.Event) {
	var invoice gostripe.Invoice
	if err := json.Unmarshal(event.Data.Raw, &invoice); err != nil {
		return
	}
	subID := ""
	if invoice.Subscription != nil {
		subID = invoice.Subscription.ID
	}
	if subID == "" {
		return
	}
	sub, err := wh.repo.GetSubscriptionByStripeID(ctx, subID)
	if err != nil || sub == nil {
		return
	}
	graceEnd := time.Now().UTC().AddDate(0, 0, wh.graceDays)
	_ = wh.repo.UpdateSubscriptionStatus(ctx, sub.TenantID, "past_due", &graceEnd)
	wh.log.Warn().Str("tenant", sub.TenantID).Time("grace_ends", graceEnd).Msg("payment failed, grace period started")
}

func (wh *WebhookHandler) onSubscriptionUpdated(ctx context.Context, event gostripe.Event) {
	var stripeSub gostripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &stripeSub); err != nil {
		return
	}
	sub, err := wh.repo.GetSubscriptionByStripeID(ctx, stripeSub.ID)
	if err != nil || sub == nil {
		return
	}
	sub.Status = string(stripeSub.Status)
	sub.CurrentPeriodStart = time.Unix(stripeSub.CurrentPeriodStart, 0)
	sub.CurrentPeriodEnd = time.Unix(stripeSub.CurrentPeriodEnd, 0)
	_ = wh.repo.UpsertSubscription(ctx, sub)
	wh.log.Info().Str("tenant", sub.TenantID).Str("status", sub.Status).Msg("subscription updated")
}

func (wh *WebhookHandler) onSubscriptionDeleted(ctx context.Context, event gostripe.Event) {
	var stripeSub gostripe.Subscription
	if err := json.Unmarshal(event.Data.Raw, &stripeSub); err != nil {
		return
	}
	sub, err := wh.repo.GetSubscriptionByStripeID(ctx, stripeSub.ID)
	if err != nil || sub == nil {
		return
	}
	_ = wh.repo.UpdateSubscriptionStatus(ctx, sub.TenantID, "cancelled", nil)
	_ = wh.router.Suspend(ctx, sub.TenantID)
	wh.log.Warn().Str("tenant", sub.TenantID).Msg("subscription deleted, tenant suspended")
}

// silence import
var _ = fmt.Sprint
