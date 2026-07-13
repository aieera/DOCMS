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

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
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
	// push is the optional mobile push transport (ADR 0117, Expo).
	// Nil disables the push channel. Set via SetPushTransport.
	push PushTransport
	log  zerolog.Logger
}

// Config is DI.
type Config struct {
	Repo  *repository.Repository
	Redis *redis.Client
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
	// Resolve recipient emails up front (one query for the whole batch).
	// Best-effort: a lookup failure degrades the email channel for this
	// delivery — in-app/push must not be held hostage by it.
	emails, emailErr := s.repo.EmailsForUsers(ctx, payload.TenantID, payload.UserIDs)
	if emailErr != nil {
		s.log.Warn().Err(emailErr).Msg("user email lookup failed; email channel degraded for this delivery")
	}
	for _, uid := range payload.UserIDs {
		// DSR verify tokens (Wave 11.4) put EMAIL ADDRESSES in
		// user_ids — there is no user row, so every UUID-typed step
		// below (Decide's snooze lookup, the notifications insert,
		// device listing) would error out and the DSR email itself
		// was never sent. Branch to a direct SMTP send up front.
		if containsAt(uid) {
			s.deliverEmailOnly(ctx, payload, uid)
			continue
		}

		// ADR 0086 — pre-delivery gate. Skip the user entirely on
		// snooze; otherwise the returned channel set tells us
		// which channels survive matrix + DND + digest. Decide()
		// is best-effort for in-app/email: on error we fall back to
		// the flat-pref behavior so a Postgres blip can't lose
		// notifications. Push is the exception — it FAILS CLOSED on
		// a Decide error (see below).
		eventPayload := map[string]any{
			"title": payload.Title, "body": payload.Body,
			"resource_type": payload.ResourceType, "resource_id": payload.ResourceID,
		}
		requested := []model.Channel{model.ChannelInApp, model.ChannelEmail, model.ChannelPush}
		consent := ParseChannelConsent(payload.Channels)
		channels, decideErr := s.Decide(ctx, payload.TenantID, uid, payload.Type,
			requested, consent, eventPayload)
		if decideErr == nil && len(channels) == 0 {
			// Active snooze (channels=nil) or every channel was
			// either disabled or folded into a digest row. Either
			// way nothing to deliver right now.
			continue
		}
		emailAllowed := containsChan(channels, model.ChannelEmail) || decideErr != nil

		pref, prefErr := s.repo.GetPreference(ctx, payload.TenantID, uid)
		if prefErr != nil || pref == nil {
			// A pref-lookup failure must not silently kill delivery
			// (a zero-value struct's false switches did exactly that
			// before — review finding). Fall back to the defaults the
			// repository would return for a missing row.
			s.log.Warn().Err(prefErr).Str("user_id", uid).Msg("pref lookup failed; using default-enabled prefs")
			pref = &model.UserPreference{TenantID: payload.TenantID, UserID: uid, EmailEnabled: true, PushEnabled: true}
		}

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

		// Mobile push (ADR 0117). Unlike in-app/email, push FAILS
		// CLOSED on a Decide error: a buzzing phone that bypassed the
		// user's snooze/DND because of a DB blip is worse than one
		// skipped push (the in-app row above still landed).
		if containsChan(channels, model.ChannelPush) && pref.PushEnabled {
			s.sendPush(ctx, notif)
		} else if decideErr != nil {
			s.log.Warn().Err(decideErr).Str("user_id", uid).Msg("push suppressed: preferences unavailable (fail-closed)")
		}

		// Email delivery. uid here is always a user UUID (address-typed
		// uids branched to deliverEmailOnly at the loop top); the batch
		// lookup above resolved it against the shared users table.
		// Deactivated / deleted / unknown / address-less users are
		// SKIPPED with a log — never an error that would fail the rest
		// of the batch (the in-app row has already landed). Send
		// failures likewise log at Error and continue.
		if emailAllowed && pref.EmailEnabled {
			sender := s.smtpSenderForTenant(ctx, payload.TenantID)
			if sender == nil || !sender.Enabled() {
				s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("would send email (SMTP not configured)")
				continue
			}
			rec, found := emails[uid]
			switch {
			case !found:
				s.log.Warn().Str("user_id", uid).Str("type", payload.Type).Msg("email skipped: no user row for id")
			case !rec.Active:
				s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("email skipped: user deactivated")
			case rec.Email == "":
				s.log.Warn().Str("user_id", uid).Str("type", payload.Type).Msg("email skipped: user has no email address")
			default:
				if err := sender.Send(rec.Email, payload.Title, payload.Body); err != nil {
					s.log.Error().Err(err).Str("user_id", uid).Str("type", payload.Type).Msg("smtp send failed")
				} else {
					s.log.Info().Str("user_id", uid).Str("type", payload.Type).Msg("email sent")
				}
			}
		}
	}
	return nil
}

// deliverEmailOnly handles recipients addressed by EMAIL rather than a
// user id (the DSR-verify path): there is no user row, so preferences,
// the in-app insert, and push don't apply — the message goes straight
// to SMTP.
func (s *Service) deliverEmailOnly(ctx context.Context, payload model.DeliveryPayload, email string) {
	sender := s.smtpSenderForTenant(ctx, payload.TenantID)
	if sender == nil || !sender.Enabled() {
		s.log.Info().Str("to", email).Str("type", payload.Type).Msg("would send email (SMTP not configured)")
		return
	}
	if err := sender.Send(email, payload.Title, payload.Body); err != nil {
		s.log.Error().Err(err).Str("to", email).Str("type", payload.Type).Msg("smtp send failed")
		return
	}
	s.log.Info().Str("to", email).Str("type", payload.Type).Msg("email sent")
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
		// Two valid shapes for msg.Data:
		//   1) the bare DeliveryPayload itself (legacy direct-publish path)
		//   2) a CloudEvents envelope with `data` containing the payload
		//      (the outbox publisher in pkg/database wraps every emit in
		//      this envelope so consumers can read tenant_id at the root)
		//
		// Earlier code unmarshalled (1) and only attempted (2) if the
		// direct unmarshal returned an error — but Go's JSON decoder
		// silently ignores fields it doesn't know, so a CloudEvents
		// envelope unmarshalled as DeliveryPayload "succeeds" with an
		// empty UserIDs slice. We then incorrectly rejected the event
		// with "notification event missing fields". Result: every
		// outbox-published notify event was Term'd by the consumer and
		// no rows ever landed in notifications.
		//
		// Fix: always look for a CloudEvents `data` object, and prefer
		// the inner payload whenever it has the required fields.
		_ = json.Unmarshal(msg.Data, &payload)
		var env map[string]any
		if err := json.Unmarshal(msg.Data, &env); err == nil {
			if data, ok := env["data"].(map[string]any); ok {
				var inner model.DeliveryPayload
				raw, _ := json.Marshal(data)
				if json.Unmarshal(raw, &inner) == nil && inner.TenantID != "" && len(inner.UserIDs) > 0 {
					payload = inner
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
