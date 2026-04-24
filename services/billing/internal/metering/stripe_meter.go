// Stripe Meter reporting. Ships each wide-format usage row as N
// meter_events (one per metric) to Stripe's /v1/billing/meter_events
// API, then stamps reported_at on the source row.
//
// Why per-metric: Stripe Meters are keyed on event_name, and a
// subscription can have multiple meters (storage, ocr, api, users,
// ai). The wide schema stores them on one row; the Stripe API
// expects one event per meter.
//
// Idempotency: stripe-go's MeterEvent API accepts an Identifier
// (timestamp-scoped dedupe key) in Payload. We use
// `<metric>:<tenant>:<period_start_unix>` — re-pushing the same
// tenant-period is a no-op on Stripe's side even if our
// reported_at write races.

package metering

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	stripego "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/billing/meterevent"

	"github.com/vaultdms/vaultdms/services/billing/internal/model"
)

// MeterName is the Stripe meter event_name per metric. Keep in sync
// with the meters configured on the Stripe account.
type MeterName string

const (
	MeterStorageGB   MeterName = "vaultdms_storage_gb"
	MeterOCRPages    MeterName = "vaultdms_ocr_pages"
	MeterAPICalls    MeterName = "vaultdms_api_calls"
	MeterActiveUsers MeterName = "vaultdms_active_users"
	MeterAITokens    MeterName = "vaultdms_ai_tokens"
)

// StripeReporter ships a usage row to Stripe Meters. stripeCustomerID
// resolver is injected so we don't query the DB from this package —
// the caller supplies the per-tenant Stripe customer id.
type StripeReporter struct {
	apiKey string
	log    zerolog.Logger
}

// NewStripeReporter constructs a reporter. Empty apiKey → soft
// no-op: the reporter logs at debug and returns nil. Useful for
// dev/test runs without a Stripe key.
func NewStripeReporter(apiKey string, log zerolog.Logger) *StripeReporter {
	return &StripeReporter{apiKey: apiKey, log: log}
}

// Report ships every metric on `rec` as a separate meter event. If
// stripeCustomerID is empty, the reporter skips (tenant has no
// Stripe customer yet). Returns nil when every event posted OR all
// were skipped; returns the first error encountered otherwise.
func (r *StripeReporter) Report(ctx context.Context, rec *model.UsageRecord, stripeCustomerID string) error {
	if r.apiKey == "" {
		r.log.Debug().Msg("stripe key not set; skipping meter report")
		return nil
	}
	if stripeCustomerID == "" {
		r.log.Debug().Str("tenant", rec.TenantID).Msg("no stripe customer; skipping")
		return nil
	}
	stripego.Key = r.apiKey

	events := []struct {
		name  MeterName
		value string
	}{
		{MeterStorageGB, fmt.Sprintf("%.6f", rec.StorageGB)},
		{MeterOCRPages, strconv.FormatInt(rec.OCRPages, 10)},
		{MeterAPICalls, strconv.FormatInt(rec.APICalls, 10)},
		{MeterActiveUsers, strconv.Itoa(rec.ActiveUsers)},
		{MeterAITokens, strconv.FormatInt(rec.AITokens, 10)},
	}

	for _, e := range events {
		if e.value == "0" || e.value == "0.000000" {
			continue // don't ship empty meters
		}
		idempotencyKey := fmt.Sprintf("%s:%s:%d", e.name, rec.TenantID, rec.PeriodStart.Unix())
		ts := rec.PeriodEnd.Unix()
		params := &stripego.BillingMeterEventParams{
			EventName:  stripego.String(string(e.name)),
			Timestamp:  stripego.Int64(ts),
			Identifier: stripego.String(idempotencyKey),
			Payload: map[string]string{
				"stripe_customer_id": stripeCustomerID,
				"value":              e.value,
			},
		}
		if _, err := meterevent.New(params); err != nil {
			return fmt.Errorf("report %s: %w", e.name, err)
		}
	}
	r.log.Info().Str("tenant", rec.TenantID).Time("period", rec.PeriodStart).Msg("stripe meters pushed")
	return nil
}

// Now returns the wall-clock time the reporter observed post-push —
// callers pass this to UpdateReportedAt so the DB stamps match.
func (r *StripeReporter) Now() time.Time { return time.Now().UTC() }
