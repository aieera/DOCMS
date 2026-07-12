// Package metering runs hourly usage collection for all tenants.
package metering

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/services/billing/internal/model"
	"github.com/aieera/sedoc/services/billing/internal/repository"
)

// Meter collects usage per tenant on a schedule.
type Meter struct {
	repo *repository.Repository
	log  zerolog.Logger
	stop chan struct{}
	// nowFn supplies the current time. Defaults to time.Now().UTC(); tests
	// override it to drive collect() across simulated hour boundaries
	// (the period bucket is now.Truncate(time.Hour)). Production behavior
	// is unchanged.
	nowFn func() time.Time
}

// New creates a Meter.
func New(repo *repository.Repository, log zerolog.Logger) *Meter {
	return &Meter{
		repo:  repo,
		log:   log,
		stop:  make(chan struct{}),
		nowFn: func() time.Time { return time.Now().UTC() },
	}
}

// Start runs the metering loop (hourly). Call in a goroutine.
func (m *Meter) Start(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	m.collect(ctx) // immediate first run
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-ticker.C:
			m.collect(ctx)
		}
	}
}

// Stop signals the loop to exit.
func (m *Meter) Stop() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

func (m *Meter) collect(ctx context.Context) {
	tenants, err := m.repo.ListAllTenants(ctx)
	if err != nil {
		m.log.Error().Err(err).Msg("metering: list tenants")
		return
	}
	now := m.nowFn()
	periodStart := now.Truncate(time.Hour)
	periodEnd := periodStart.Add(time.Hour)
	since := periodStart.AddDate(0, -1, 0) // 30-day window for OCR/users

	for _, tid := range tenants {
		// Meter-read failures skip the tenant's insert for this cycle
		// rather than overwriting a good record with zeros — and they
		// are LOGGED: the silent discards here are how a broken column
		// reference metered active_users as 0 indefinitely without a
		// single log line (Wave A.1.c).
		storageGB, err := m.repo.MeterStorage(ctx, tid)
		if err != nil {
			m.log.Error().Err(err).Str("tenant", tid).Msg("metering: storage")
			continue
		}
		ocrPages, err := m.repo.MeterOCRPages(ctx, tid, since)
		if err != nil {
			m.log.Error().Err(err).Str("tenant", tid).Msg("metering: ocr pages")
			continue
		}
		activeUsers, err := m.repo.MeterActiveUsers(ctx, tid, since)
		if err != nil {
			m.log.Error().Err(err).Str("tenant", tid).Msg("metering: active users")
			continue
		}

		// AI tokens — read from Redis aggregate (set by intelligence service).
		var aiTokens int64 // placeholder; would read from redis llm_usage:{tid}

		record := &model.UsageRecord{
			TenantID:    tid,
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
			StorageGB:   storageGB,
			OCRPages:    ocrPages,
			APICalls:    0, // populated from gateway metrics
			ActiveUsers: activeUsers,
			AITokens:    aiTokens,
		}
		if err := m.repo.InsertUsage(ctx, record); err != nil {
			m.log.Error().Err(err).Str("tenant", tid).Msg("insert usage")
		}
	}
	m.log.Info().Int("tenants", len(tenants)).Msg("metering: collection complete")
}
