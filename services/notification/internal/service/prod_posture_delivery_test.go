//go:build integration && prodposture
// +build integration,prodposture

// Wave A.1 (issue #73) — the notification DELIVERY path under prod
// NOBYPASSRLS, end to end through the real Service.Deliver:
//
//   - control user: the notification lands (before the fix, the
//     FORCE-RLS notifications INSERT itself was rejected — nobody
//     received anything, which made "snoozed users get nothing"
//     vacuously true and preference gating untestable);
//   - snoozed user: provably receives NOTHING during the snooze;
//   - DND honored: two users with identical digest-enabled email cells,
//     one inside a DND window — the digest row appears only for the
//     user outside DND (email for UUID users is log-only pending Wave
//     B.6, so digest rows are the DB-observable channel effect);
//   - digest respects prefs: the digest cell folds the channel into
//     notification_digests instead of immediate delivery;
//   - isolation: tenant A's '*' snooze must NOT suppress the same user
//     id under tenant B.
package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
	"github.com/aieera/sedoc/services/notification/internal/service"
)

// deliveryFixtureDDL — verbatim shapes of the FORCE-RLS tables the
// delivery path touches (document 000001 TABLE 32/33 + notification
// 000001 renames + document 000034), plus the deliberately-no-RLS flat
// prefs table and FK parents.
const deliveryFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY,
	deleted_at TIMESTAMPTZ
);
CREATE TABLE users (
	tenant_id UUID NOT NULL REFERENCES organizations(id),
	id        UUID NOT NULL,
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
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE  ROW LEVEL SECURITY;
CREATE POLICY notifications_tenant_isolation ON notifications
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notifications_tenant_isolation_insert ON notifications
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE notification_preferences (
	tenant_id      UUID NOT NULL REFERENCES organizations(id),
	user_id        UUID NOT NULL,
	channel        TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	is_enabled     BOOLEAN NOT NULL DEFAULT true,
	digest_enabled BOOLEAN NOT NULL DEFAULT false,
	PRIMARY KEY (tenant_id, user_id, channel, event_type)
);
ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_preferences FORCE  ROW LEVEL SECURITY;
CREATE POLICY notif_prefs_tenant_isolation ON notification_preferences
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notif_prefs_tenant_isolation_insert ON notification_preferences
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

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
ALTER TABLE notification_snoozes ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_snoozes FORCE  ROW LEVEL SECURITY;
CREATE POLICY notification_snoozes_tenant_isolation ON notification_snoozes
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_snoozes_tenant_isolation_insert ON notification_snoozes
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

CREATE TABLE notification_dnd (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	user_id    UUID NOT NULL,
	dnd_start  TIME NOT NULL,
	dnd_end    TIME NOT NULL,
	timezone   TEXT NOT NULL DEFAULT 'UTC',
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, user_id)
);
ALTER TABLE notification_dnd ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_dnd FORCE  ROW LEVEL SECURITY;
CREATE POLICY notification_dnd_tenant_isolation ON notification_dnd
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_dnd_tenant_isolation_insert ON notification_dnd
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

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
CREATE UNIQUE INDEX idx_notification_digests_open
	ON notification_digests (tenant_id, user_id, event_type, channel)
	WHERE flushed_at IS NULL;
ALTER TABLE notification_digests ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digests FORCE  ROW LEVEL SECURITY;
CREATE POLICY notification_digests_tenant_isolation ON notification_digests
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY notification_digests_tenant_isolation_insert ON notification_digests
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

-- Flat prefs: deliberately NO RLS (notification 000003 convention).
CREATE TABLE notification_user_prefs (
	tenant_id       UUID NOT NULL,
	user_id         UUID NOT NULL,
	email_enabled   BOOLEAN NOT NULL DEFAULT true,
	push_enabled    BOOLEAN NOT NULL DEFAULT true,
	slack_enabled   BOOLEAN NOT NULL DEFAULT false,
	sms_enabled     BOOLEAN NOT NULL DEFAULT false,
	quiet_hours_from TIME,
	quiet_hours_to   TIME,
	PRIMARY KEY (tenant_id, user_id)
);

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
`

func TestProdPosture_NotificationDelivery(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, deliveryFixtureDDL)
	require.NoError(t, err)

	redisURL, redisCleanup, err := testutil.NewRedisContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(redisCleanup)
	ropts, err := redis.ParseURL(redisURL)
	require.NoError(t, err)
	rdb := redis.NewClient(ropts)
	t.Cleanup(func() { _ = rdb.Close() })

	tenantA := uuid.Must(uuid.NewV7())
	tenantB := uuid.Must(uuid.NewV7())
	control := uuid.Must(uuid.NewV7())
	snoozed := uuid.Must(uuid.NewV7())
	dndUser := uuid.Must(uuid.NewV7())
	digestUser := uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	repo := repository.New(db.App) // exactly as cmd/server wires it
	svc := service.New(service.Config{Repo: repo, Redis: rdb, Logger: zerolog.Nop()})

	const eventType = "document.shared"
	deliver := func(tenant, user uuid.UUID) {
		require.NoError(t, svc.Deliver(ctx, model.DeliveryPayload{
			TenantID: tenant.String(),
			UserIDs:  []string{user.String()},
			Type:     eventType,
			Title:    "Doc shared with you",
			Body:     "body",
		}))
	}
	countRows := func(tenant, user uuid.UUID) int {
		var n int
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM notifications WHERE user_id = $1`, user).Scan(&n)
		}))
		return n
	}
	countDigests := func(tenant, user uuid.UUID) int {
		var n int
		require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT COUNT(*) FROM notification_digests WHERE user_id = $1`, user).Scan(&n)
		}))
		return n
	}

	t.Run("ControlUserReceives", func(t *testing.T) {
		deliver(tenantA, control)
		require.Equal(t, 1, countRows(tenantA, control),
			"the delivery path itself must work under NOBYPASSRLS (the notifications INSERT was rejected pre-fix)")
	})

	t.Run("SnoozedUserReceivesNothing", func(t *testing.T) {
		_, err := repo.CreateSnooze(ctx, model.Snooze{
			TenantID: tenantA.String(), UserID: snoozed.String(), EventType: "*",
		}, time.Hour)
		require.NoError(t, err, "snooze write must work under NOBYPASSRLS")

		deliver(tenantA, snoozed)
		require.Zero(t, countRows(tenantA, snoozed),
			"a snoozed user must provably receive NOTHING during the snooze")
		require.Zero(t, countDigests(tenantA, snoozed))
	})

	t.Run("DigestRespectsPrefs_And_DNDHonored", func(t *testing.T) {
		// Identical digest-enabled email cells for two users; only
		// dndUser sits inside a DND window covering "now".
		for _, u := range []uuid.UUID{digestUser, dndUser} {
			require.NoError(t, repo.UpsertCell(ctx, model.PrefCell{
				TenantID: tenantA.String(), UserID: u.String(),
				Channel: string(model.ChannelEmail), EventType: eventType,
				IsEnabled: true, DigestEnabled: true,
			}))
		}
		now := time.Now().UTC()
		start := now.Add(-1 * time.Hour)
		end := now.Add(1 * time.Hour)
		require.NoError(t, repo.UpsertDND(ctx, model.DND{
			TenantID: tenantA.String(), UserID: dndUser.String(),
			DNDStart: start.Format("15:04"), DNDEnd: end.Format("15:04"),
			Timezone: "UTC",
		}))

		deliver(tenantA, digestUser)
		deliver(tenantA, dndUser)

		require.Equal(t, 1, countDigests(tenantA, digestUser),
			"digest-enabled email cell must fold the event into notification_digests")
		require.Zero(t, countRows(tenantA, digestUser),
			"the digest cell replaces immediate delivery for that channel")
		require.Zero(t, countDigests(tenantA, dndUser),
			"DND must suppress the (non-in_app) email channel before it reaches the digest")
		// Both users' matrices list ONLY the email cell for this exact
		// event type, and an exact-match matrix disables unlisted
		// channels (exact rows win over the in_app/push default) — so
		// neither user gets an in_app row here. The ControlUser subtest
		// already proves immediate in_app delivery works under prod
		// posture; the point of THIS case is that DND suppresses the
		// email channel for dndUser while digestUser folds to a digest.
		require.Zero(t, countRows(tenantA, dndUser))
	})

	t.Run("SnoozeIsolationAcrossTenants", func(t *testing.T) {
		// The SAME user id snoozed in tenant A must still receive in
		// tenant B — A's snooze row must be invisible to B's context.
		deliver(tenantB, snoozed)
		require.Equal(t, 1, countRows(tenantB, snoozed),
			"tenant A's snooze must not suppress the same user id under tenant B")
		require.Zero(t, countRows(tenantA, snoozed),
			"tenant A's snooze still holds in tenant A")
	})
}
