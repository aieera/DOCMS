//go:build integration
// +build integration

// The email channel of the normal (non-DSR) delivery flow, end to end:
// user-id → email resolution against the shared users table, gated by
// the preference pipeline.
//
// Before this fix the resolution was a stub that logged "email skipped:
// user-id not an email (lookup pending)" — with SMTP fully configured,
// NO in-app-flow notification ever produced an email; only DSR mail
// (which carries a raw address) went out. Pinned here:
//
//   - active user with an address → exactly one email to that address;
//   - deactivated user → skipped with a log, batch NOT errored (the
//     active user in the same batch still gets mail + in-app rows land);
//   - snoozed user → fully suppressed (no email, no in-app row);
//   - flat email_enabled=false → no email, in-app row still lands.
package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
	"github.com/aieera/sedoc/services/notification/internal/service"
)

// emailFixtureDDL — the delivery path's tables plus the users columns
// the resolver reads (email/status/deleted_at). Verbatim shapes from
// document 000001 (+ notification 000003 flat prefs), no RLS: tenant
// scoping under prod posture is the #91 prod-posture suite's job.
const emailFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY,
	deleted_at TIMESTAMPTZ
);
CREATE TABLE users (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	id         UUID NOT NULL,
	email      TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'active',
	deleted_at TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notifications (
	id            UUID NOT NULL,
	tenant_id     UUID NOT NULL REFERENCES organizations(id),
	user_id       UUID NOT NULL,
	type          TEXT NOT NULL,
	title         TEXT NOT NULL,
	body          TEXT NOT NULL DEFAULT '',
	resource_type TEXT,
	resource_id   TEXT,
	channel       TEXT NOT NULL DEFAULT 'in_app',
	read          BOOLEAN NOT NULL DEFAULT false,
	delivered_at  TIMESTAMPTZ,
	read_at       TIMESTAMPTZ,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_preferences (
	tenant_id      UUID NOT NULL REFERENCES organizations(id),
	user_id        UUID NOT NULL,
	channel        TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	is_enabled     BOOLEAN NOT NULL DEFAULT true,
	digest_enabled BOOLEAN NOT NULL DEFAULT false,
	PRIMARY KEY (tenant_id, user_id, channel, event_type)
);
CREATE TABLE notification_snoozes (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	id         UUID NOT NULL DEFAULT gen_random_uuid(),
	user_id    UUID NOT NULL,
	event_type TEXT NOT NULL,
	until_at   TIMESTAMPTZ NOT NULL,
	reason     TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_dnd (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	user_id    UUID NOT NULL,
	dnd_start  TIME NOT NULL,
	dnd_end    TIME NOT NULL,
	timezone   TEXT NOT NULL DEFAULT 'UTC',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, user_id)
);
CREATE TABLE notification_digests (
	tenant_id      UUID NOT NULL REFERENCES organizations(id),
	id             UUID NOT NULL DEFAULT gen_random_uuid(),
	user_id        UUID NOT NULL,
	event_type     TEXT NOT NULL,
	channel        TEXT NOT NULL,
	events         JSONB NOT NULL DEFAULT '[]'::jsonb,
	count          INT NOT NULL DEFAULT 0,
	flush_after_at TIMESTAMPTZ NOT NULL,
	flushed_at     TIMESTAMPTZ,
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE notification_user_prefs (
	tenant_id        UUID NOT NULL,
	user_id          UUID NOT NULL,
	email_enabled    BOOLEAN NOT NULL DEFAULT true,
	push_enabled     BOOLEAN NOT NULL DEFAULT true,
	slack_enabled    BOOLEAN NOT NULL DEFAULT false,
	sms_enabled      BOOLEAN NOT NULL DEFAULT false,
	quiet_hours_from TIME,
	quiet_hours_to   TIME,
	PRIMARY KEY (tenant_id, user_id)
);
`

// recordingSender captures every Send; Enabled is always true, so the
// resolution path (not SMTP config) is what's under test.
type recordingSender struct {
	mu    sync.Mutex
	sends []sentMail
}

type sentMail struct{ To, Subject, Body string }

func (r *recordingSender) Send(to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, sentMail{to, subject, body})
	return nil
}
func (r *recordingSender) Enabled() bool { return true }
func (r *recordingSender) all() []sentMail {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentMail(nil), r.sends...)
}

type emailHarness struct {
	ctx    context.Context
	svc    *service.Service
	repo   *repository.Repository
	pool   *pgxpool.Pool
	smtp   *recordingSender
	tenant uuid.UUID
}

func newEmailHarness(t *testing.T) *emailHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, emailFixtureDDL)
	require.NoError(t, err)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	tenant := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	smtp := &recordingSender{}
	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Redis: rdb, SMTP: smtp, Logger: zerolog.Nop()})

	return &emailHarness{ctx: ctx, svc: svc, repo: repo, pool: pool, smtp: smtp, tenant: tenant}
}

func (h *emailHarness) addUser(t *testing.T, email, status string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := h.pool.Exec(h.ctx,
		`INSERT INTO users (tenant_id, id, email, status) VALUES ($1,$2,$3,$4)`,
		h.tenant, id, email, status)
	require.NoError(t, err)
	// ADR 0086 default policy: with no matrix rows only in_app+push are
	// on. The email channel is a user opt-in — set the matrix cell the
	// way the prefs UI does, so these tests exercise delivery THROUGH
	// the (now-working, A.1.d) preference matrix.
	require.NoError(t, h.repo.UpsertCell(h.ctx, model.PrefCell{
		TenantID: h.tenant.String(), UserID: id.String(),
		Channel: string(model.ChannelEmail), EventType: "document.shared",
		IsEnabled: true,
	}))
	return id
}

func (h *emailHarness) inAppCount(t *testing.T, user uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT count(*) FROM notifications WHERE tenant_id=$1 AND user_id=$2`,
		h.tenant, user).Scan(&n))
	return n
}

func (h *emailHarness) deliver(t *testing.T, users ...uuid.UUID) {
	t.Helper()
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.String()
	}
	require.NoError(t, h.svc.Deliver(h.ctx, model.DeliveryPayload{
		TenantID: h.tenant.String(),
		UserIDs:  ids,
		Type:     "document.shared",
		Title:    "Doc shared with you",
		Body:     "check it out",
	}))
}

func TestEmailDelivery_ActiveUserGetsEmailAtRightAddress(t *testing.T) {
	h := newEmailHarness(t)
	alice := h.addUser(t, "alice@example.test", "active")

	h.deliver(t, alice)

	sends := h.smtp.all()
	require.Len(t, sends, 1, "a non-DSR in-app notification must produce exactly one email")
	require.Equal(t, "alice@example.test", sends[0].To, "rendered to the RIGHT address")
	require.Equal(t, "Doc shared with you", sends[0].Subject)
	require.Equal(t, 1, h.inAppCount(t, alice), "in-app row lands too")
}

func TestEmailDelivery_DeactivatedUserSkippedWithoutFailingBatch(t *testing.T) {
	h := newEmailHarness(t)
	alice := h.addUser(t, "alice@example.test", "active")
	ghost := h.addUser(t, "ghost@example.test", "deactivated")

	// Deactivated user FIRST in the batch — a skip that errored would
	// starve alice behind it.
	h.deliver(t, ghost, alice)

	sends := h.smtp.all()
	require.Len(t, sends, 1, "deactivated user is skipped, active user still mailed")
	require.Equal(t, "alice@example.test", sends[0].To)
}

func TestEmailDelivery_SnoozedUserFullySuppressed(t *testing.T) {
	h := newEmailHarness(t)
	bob := h.addUser(t, "bob@example.test", "active")
	_, err := h.repo.CreateSnooze(h.ctx, model.Snooze{
		TenantID: h.tenant.String(), UserID: bob.String(), EventType: "*",
	}, time.Hour)
	require.NoError(t, err)

	h.deliver(t, bob)

	require.Empty(t, h.smtp.all(), "snoozed user must receive no email")
	require.Zero(t, h.inAppCount(t, bob), "snoozed user must receive nothing at all")
}

func TestEmailDelivery_FlatPrefDisablesEmailOnly(t *testing.T) {
	h := newEmailHarness(t)
	carol := h.addUser(t, "carol@example.test", "active")
	// Through the real UpsertPreference — its int→TIME encoding and
	// GetPreference's TIME→int scan were BOTH broken, so the flat
	// email_enabled=false switch had never survived a write+read round
	// trip (reads errored into the default-enabled fallback).
	require.NoError(t, h.repo.UpsertPreference(h.ctx, &model.UserPreference{
		TenantID: h.tenant.String(), UserID: carol.String(),
		EmailEnabled: false, PushEnabled: true,
	}))

	h.deliver(t, carol)

	require.Empty(t, h.smtp.all(), "email_enabled=false must suppress the email channel")
	require.Equal(t, 1, h.inAppCount(t, carol), "in-app channel unaffected by the email pref")
}
