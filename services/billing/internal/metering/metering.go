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
}

// New creates a Meter.
func New(repo *repository.Repository, log zerolog.Logger) *Meter {
	return &Meter{repo: repo, log: log, stop: make(chan struct{})}
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
	now := time.Now().UTC()
	periodStart := now.Truncate(time.Hour)
	periodEnd := periodStart.Add(time.Hour)
	since := periodStart.AddDate(0, -1, 0) // 30-day window for OCR/users

	for _, tid := range tenants {
		storageGB, _ := m.repo.MeterStorage(ctx, tid)
		ocrPages, _ := m.repo.MeterOCRPages(ctx, tid, since)
		activeUsers, _ := m.repo.MeterActiveUsers(ctx, tid, since)

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
