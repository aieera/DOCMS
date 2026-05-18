// Package service contains notification business logic: NATS consumption,
// multi-channel delivery, preference checks, Redis pub/sub for real-time.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/notification/internal/model"
	"github.com/vaultdms/vaultdms/services/notification/internal/repository"
)

// Service is the notification facade.
type Service struct {
	repo *repository.Repository
	rdb  *redis.Client
	smtp SMTPSender
	// tenantSMTP + smtpSealKey are set via SetTenantSMTPDeps. When a
	// payload's tenant has a saved tenant_smtp_configs row, smtpSenderForTenant
	// builds a per-tenant sender on the fly instead of using s.smtp.
	tenantSMTP  TenantSMTPReader
	smtpSealKey []byte
	log         zerolog.Logger
}

// Config is DI.
type Config struct {
	Repo   *repository.Repository
	Redis  *redis.Client
	// SMTP is optional. nil → email channel disabled; the service
	// still logs "would send email" as before for observability.
	SMTP   SMTPSender
	Logger zerolog.Logger
}

// New creates a Service.
func New(cfg Config) *Service {
	return &Service{repo: cfg.Repo, rdb: cfg.Redis, smtp: cfg.SMTP, log: cfg.Logger}
}

// Deliver creates in-app notifications and publishes to Redis for real-time.
func (s *Service) Deliver(ctx context.Context, payload model.DeliveryPayload) error {
	now := time.Now().UTC()
	for _, uid := range payload.UserIDs {
		// ADR 0086 — pre-delivery gate. Skip the user entirely on
		// snooze; otherwise the returned channel set tells us
		// which channels survive matrix + DND + digest. Decide()
		// is best-effort: on error we fall back to the legacy
		// flat-pref behavior so a Postgres blip can't lose
		// notifications.
		eventPayload := map[string]any{
			"title": payload.Title, "body": payload.Body,
			"resource_type": payload.ResourceType, "resource_id": payload.ResourceID,
		}
		channels, decideErr := s.Decide(ctx, payload.TenantID, uid, payload.Type,
			[]model.Channel{model.ChannelInApp, model.ChannelEmail}, eventPayload)
		if decideErr == nil && len(channels) == 0 {
			// Active snooze (channels=nil) or every channel was
			// either disabled or folded into a digest row. Either
			// way nothing to deliver right now.
			continue
		}
		emailAllowed := containsChan(channels, model.ChannelEmail) || decideErr != nil

		pref, _ := s.repo.GetPreference(ctx, payload.TenantID, uid)

		// In-app always.
		notif := &model.Notification{
			TenantID: payload.TenantID, UserID: uid, Type: payload.Type,
			Title: payload.Title, Body: payload.Body,
			ResourceType: payload.ResourceType, ResourceID: payload.ResourceID,
			Channel: model.ChannelInApp, CreatedAt: now,
		}
		if err := s.repo.Insert(ctx, notif); err != nil {
			s.log.Error().Err(err).Str("user_id", uid).Msg("insert notification")
			continue
		}

		// Real-time via Redis pub/sub.
		redisChan := fmt.Sprintf("notif:%s:%s", payload.TenantID, uid)
		msg, _ := json.Marshal(notif)
		if err := s.rdb.Publish(ctx, redisChan, msg).Err(); err != nil {
			s.log.Warn().Err(err).Msg("redis publish")
		}

		// Email delivery. Wave 12.1: real SMTP when configured;
		// otherwise the pre-existing "would send" log retains the
		// observability breadcrumb for dev. Failure to send is
		// logged at Error; we don't fail the whole Deliver loop
		// because in-app notification has already landed.
		if emailAllowed && pref != nil && pref.EmailEnabled {
			sender := s.smtpSenderForTenant(ctx, payload.TenantID)
			if sender != nil && sender.Enabled() {
				// payload.UserIDs carries user UUIDs today; the
				// DSR-verify publisher (Wave 11.4) passes email
				// addresses instead. Treat the string as an email
				// when it contains '@'; otherwise we'd need a DB
				// lookup to resolve uid → email. That lookup is a
				// Wave 12 follow-up; for now only DSR tokens
				// reach the SMTP path (they already carry emails).
				if containsAt(uid) {
					if err := sender.Send(uid, payload.Title, payload.Body); err != nil {
						s.log.Error().Err(err).Str("to", uid).Str("type", payload.Type).Msg("smtp send failed")
					} else {
						s.log.Info().Str("to", uid).Str("type", payload.Type).Msg("email sent")
					}
				} else {
					s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("email skipped: user-id not an email (lookup pending)")
				}
			} else {
				s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("would send email (SMTP not configured)")
			}
		}
	}
	return nil
}

// List returns notifications for a user.
func (s *Service) List(ctx context.Context, tenantID, userID string, readFilter *bool, limit int) ([]*model.Notification, error) {
	return s.repo.List(ctx, tenantID, userID, readFilter, limit)
}

// MarkRead marks one notification as read.
func (s *Service) MarkRead(ctx context.Context, tenantID, userID, id string) error {
	return s.repo.MarkRead(ctx, tenantID, userID, id)
}

// MarkAllRead marks all notifications as read.
func (s *Service) MarkAllRead(ctx context.Context, tenantID, userID string) error {
	return s.repo.MarkAllRead(ctx, tenantID, userID)
}

// UnreadCount returns unread count.
func (s *Service) UnreadCount(ctx context.Context, tenantID, userID string) (int, error) {
	return s.repo.UnreadCount(ctx, tenantID, userID)
}

// GetPreference returns user prefs.
func (s *Service) GetPreference(ctx context.Context, tenantID, userID string) (*model.UserPreference, error) {
	return s.repo.GetPreference(ctx, tenantID, userID)
}

// UpdatePreference saves user prefs.
func (s *Service) UpdatePreference(ctx context.Context, p *model.UserPreference) error {
	return s.repo.UpsertPreference(ctx, p)
}

// ----- ADR 0086 pass-throughs ------------------------------------
// Thin wrappers so the handler talks to one Service surface; the
// business logic for each lives in the repo (CRUD) or decide.go
// (the gating pipeline).

func (s *Service) ListMatrix(ctx context.Context, tenantID, userID string) ([]model.PrefCell, error) {
	return s.repo.ListMatrix(ctx, tenantID, userID)
}
func (s *Service) ReplaceMatrix(ctx context.Context, tenantID, userID string, cells []model.PrefCell) error {
	return s.repo.ReplaceMatrix(ctx, tenantID, userID, cells)
}
func (s *Service) UpsertCell(ctx context.Context, c model.PrefCell) error {
	return s.repo.UpsertCell(ctx, c)
}
func (s *Service) ListActiveSnoozes(ctx context.Context, tenantID, userID string) ([]model.Snooze, error) {
	return s.repo.ListActiveSnoozes(ctx, tenantID, userID)
}
func (s *Service) CreateSnooze(ctx context.Context, sn model.Snooze, dur time.Duration) (*model.Snooze, error) {
	return s.repo.CreateSnooze(ctx, sn, dur)
}
func (s *Service) DeleteSnooze(ctx context.Context, tenantID, userID, id string) error {
	return s.repo.DeleteSnooze(ctx, tenantID, userID, id)
}
func (s *Service) GetDND(ctx context.Context, tenantID, userID string) (*model.DND, error) {
	return s.repo.GetDND(ctx, tenantID, userID)
}
func (s *Service) UpsertDND(ctx context.Context, d model.DND) error {
	return s.repo.UpsertDND(ctx, d)
}
func (s *Service) DeleteDND(ctx context.Context, tenantID, userID string) error {
	return s.repo.DeleteDND(ctx, tenantID, userID)
}

// containsAt reports whether s looks like an email address. Used
// by the email-channel path to distinguish "this is a user UUID"
// (looked up via DB — not yet wired) from "this is already an
// email" (DSR verify path).
func containsChan(set []model.Channel, c model.Channel) bool {
	for _, x := range set {
		if x == c {
			return true
		}
	}
	return false
}

func containsAt(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '@' {
			return true
		}
	}
	return false
}

// StartConsumer subscribes to dms.notify.> events. `parent` is the
// service lifecycle ctx; SIGTERM cancellation cascades into every
// in-flight handler (Wave 6 Prompt 6.4).
func (s *Service) StartConsumer(parent context.Context, js nats.JetStreamContext) error {
	if parent == nil {
		parent = context.Background()
	}
	_, err := js.Subscribe("dms.notify.>", func(msg *nats.Msg) {
		var payload model.DeliveryPayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			// Try CloudEvents envelope.
			var env map[string]any
			if err2 := json.Unmarshal(msg.Data, &env); err2 == nil {
				if data, ok := env["data"].(map[string]any); ok {
					raw, _ := json.Marshal(data)
					_ = json.Unmarshal(raw, &payload)
				}
			}
		}
		if payload.TenantID == "" || len(payload.UserIDs) == 0 {
			s.log.Warn().Str("subject", msg.Subject).Msg("notification event missing fields")
			_ = msg.Term()
			return
		}
		ctx, cancel := context.WithTimeout(parent, 15*time.Second)
		defer cancel()
		if corrID := msg.Header.Get("correlation-id"); corrID != "" {
			ctx = auth.SetCorrelationID(ctx, corrID)
		}
		if err := s.Deliver(ctx, payload); err != nil {
			s.log.Error().Err(err).Msg("deliver notification")
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	}, nats.Durable("notification-all"), nats.ManualAck(), nats.MaxDeliver(5))
	if err != nil {
		return fmt.Errorf("subscribe dms.notify.>: %w", err)
	}
	s.log.Info().Msg("notification consumer subscribed to dms.notify.>")
	return nil
}
