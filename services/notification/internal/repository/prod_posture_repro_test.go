//go:build integration && prodposture
// +build integration,prodposture

// Prod-only RLS repro — notification preference reads silently no-op.
//
// BUG (audit-verified, STATE_OF_THE_PROJECT 2026-07-03): the notification
// service queries the ADR-0086 preference tables (notification_preferences,
// notification_snoozes, notification_dnd, notification_digests — all
// ENABLE + FORCE ROW LEVEL SECURITY, created by document migrations
// 000001/000034) on the RAW pgx pool without ever setting
// app.current_tenant (services/notification/internal/repository/prefs.go).
// Dev and the standard integration lane connect as the testcontainer
// superuser (BYPASSRLS), so the reads work there. Under the production
// posture — dms_app role with NOBYPASSRLS (helm postInit / migration
// 000065) — the isolation policy's current_setting() is NULL and every
// read fails CLOSED to 0 rows. None of these reads error on 0 rows: the
// matrix lookup returns the "use defaults" empty slice, IsSnoozed returns
// false, GetDND returns nil. Result in prod: snooze, DND, and the
// channel matrix are silently ignored and every notification is
// delivered as if the user had no preferences at all.
//
// This test constructs the REAL prefs repository over a NOBYPASSRLS
// dms_app pool exactly as production does, seeds rows tenant-correctly,
// and asserts the CONTENT the reads must return (correct post-fix
// behavior). Today it FAILS in the RLS fail-closed mode described above.
//
// Tracking: https://github.com/aieera/DOCMS/issues/73
// Allow-listed in ci/prod-posture-allowlist.txt until the Wave A fix
// (route the prefs repository through database.WithTenantTx) lands.
//
// Fixture note: NewProdPostureDB is given an empty migrationsDir on
// purpose. The pref tables are NOT in this service's migrations — they
// ship in services/document/migrations (000001 table 32 + 000034), and
// that chain is currently broken on a clean DB at 000021 (STATE
// 2026-07-03 "Migration blocker"); the notification service's own
// migrations only ALTER document-owned tables and cannot apply to an
// empty database either. The DDL below is copied verbatim from those
// document migrations (minus the updated_at triggers, which are not
// load-bearing here), so the repository runs against the
// schema-of-record shape including the exact FORCE-RLS policies.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/model"
	"github.com/aieera/sedoc/services/notification/internal/repository"
)

func TestProdPosture_NotificationPrefs(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")

	// --- Schema-of-record fixture (see fixture note in the header). ----
	// Parents first: the pref tables FK organizations(id) and
	// users(tenant_id, id). Minimal shapes are enough — referential-
	// integrity checks bypass RLS, and these two are seeded via Super.
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY);
		CREATE TABLE users (
			tenant_id UUID NOT NULL,
			id        UUID NOT NULL,
			PRIMARY KEY (tenant_id, id)
		);

		-- document migration 000001, TABLE 32 (+ 000034 digest_enabled).
		CREATE TABLE notification_preferences (
			tenant_id    UUID NOT NULL REFERENCES organizations(id),
			user_id      UUID NOT NULL,
			channel      TEXT NOT NULL CHECK (channel IN ('email', 'push', 'in_app', 'slack', 'teams', 'sms')),
			event_type   TEXT NOT NULL,
			is_enabled   BOOLEAN NOT NULL DEFAULT true,
			quiet_start  TIME,
			quiet_end    TIME,
			updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
			digest_enabled BOOLEAN NOT NULL DEFAULT FALSE,
			PRIMARY KEY (tenant_id, user_id, channel, event_type),
			FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
		);
		ALTER TABLE notification_preferences ENABLE ROW LEVEL SECURITY;
		ALTER TABLE notification_preferences FORCE  ROW LEVEL SECURITY;
		CREATE POLICY notif_prefs_tenant_isolation ON notification_preferences
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY notif_prefs_tenant_isolation_insert ON notification_preferences
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- document migration 000034 §2.
		CREATE TABLE notification_snoozes (
			tenant_id   UUID         NOT NULL REFERENCES organizations(id),
			id          UUID         NOT NULL DEFAULT gen_random_uuid(),
			user_id     UUID         NOT NULL,
			event_type  TEXT         NOT NULL,
			until_at    TIMESTAMPTZ  NOT NULL,
			reason      TEXT,
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE,
			CONSTRAINT notification_snoozes_until CHECK (until_at > created_at)
		);
		ALTER TABLE notification_snoozes ENABLE ROW LEVEL SECURITY;
		ALTER TABLE notification_snoozes FORCE  ROW LEVEL SECURITY;
		CREATE POLICY notification_snoozes_tenant_isolation ON notification_snoozes
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY notification_snoozes_tenant_isolation_insert ON notification_snoozes
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- document migration 000034 §3.
		CREATE TABLE notification_dnd (
			tenant_id    UUID         NOT NULL REFERENCES organizations(id),
			user_id      UUID         NOT NULL,
			dnd_start    TIME         NOT NULL,
			dnd_end      TIME         NOT NULL,
			timezone     TEXT         NOT NULL DEFAULT 'UTC',
			created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, user_id),
			FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id) ON DELETE CASCADE
		);
		ALTER TABLE notification_dnd ENABLE ROW LEVEL SECURITY;
		ALTER TABLE notification_dnd FORCE  ROW LEVEL SECURITY;
		CREATE POLICY notification_dnd_tenant_isolation ON notification_dnd
			USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY notification_dnd_tenant_isolation_insert ON notification_dnd
			FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);

		-- Tables created after NewProdPostureDB's blanket grant need their own.
		GRANT SELECT, INSERT, UPDATE, DELETE
			ON organizations, users, notification_preferences,
			   notification_snoozes, notification_dnd
			TO dms_app;
	`)
	require.NoError(t, err, "fixture DDL")

	tenantID := uuid.Must(uuid.NewV7())
	userID := uuid.Must(uuid.NewV7())

	// FK parents via the superuser pool — same split ops tooling has.
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenantID)
	require.NoError(t, err, "seed organization")
	_, err = db.Super.Exec(ctx, `INSERT INTO users (tenant_id, id) VALUES ($1, $2)`, tenantID, userID)
	require.NoError(t, err, "seed user")

	// Seed the user's preferences TENANT-CORRECTLY as the dms_app role:
	// WithTenantTx sets app.current_tenant, so the FORCE-RLS insert
	// policies accept the rows. This is the write path the fix must adopt.
	//
	// A dedicated app-role pool (same DSN, same armed posture gate) is
	// used for seeding and closed before the repro reads. Committing
	// set_config(..., local=true) leaves the custom GUC defined as ''
	// on that pooled session (Postgres placeholder-GUC quirk), and a
	// later raw read on the SAME connection then errors with 22P02
	// (''::uuid) instead of fail-closing to 0 rows. Both modes are the
	// same underlying bug, but this test pins the deterministic
	// fresh-session mode — current_setting() IS NULL → 0 rows — which
	// is what a prefs read on a never-tenant-scoped connection sees.
	seedPool, err := database.NewPool(ctx, db.AppDSN, database.DefaultPoolConfig())
	require.NoError(t, err, "seeding app pool")
	require.NoError(t, database.WithTenantTx(ctx, seedPool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_preferences (tenant_id, user_id, channel, event_type, is_enabled, digest_enabled)
			VALUES ($1, $2, 'email', 'document.shared', false, true)`,
			tenantID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_snoozes (tenant_id, user_id, event_type, until_at, reason)
			VALUES ($1, $2, '*', now() + interval '1 hour', 'focus time')`,
			tenantID, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO notification_dnd (tenant_id, user_id, dnd_start, dnd_end, timezone)
			VALUES ($1, $2, '22:00'::time, '07:00'::time, 'UTC')`,
			tenantID, userID)
		return err
	}), "tenant-correct seeding must succeed under FORCE RLS")
	seedPool.Close()

	// The REAL repository over the NOBYPASSRLS app pool — exactly how
	// production wires it (cmd/server passes the shared raw pool).
	repo := repository.New(db.App)

	// --- Flagged read 1: matrix lookup (prefs.go ListMatrix). ----------
	// The bug's failure mode is NOT an error: it is an empty slice that
	// callers treat as "use defaults". Assert on CONTENT so the
	// fail-closed empty result fails the test.
	cells, err := repo.ListMatrix(ctx, tenantID.String(), userID.String())
	require.NoError(t, err, "ListMatrix must not error")
	if assert.Len(t, cells, 1,
		"ListMatrix must return the seeded matrix cell; 0 rows means the raw-pool "+
			"read fail-closed under FORCE RLS + NOBYPASSRLS and the user's channel "+
			"matrix was silently ignored") {
		assert.Equal(t, model.PrefCell{
			TenantID:      tenantID.String(),
			UserID:        userID.String(),
			Channel:       "email",
			EventType:     "document.shared",
			IsEnabled:     false,
			DigestEnabled: true,
		}, cells[0])
	}

	// --- Flagged read 2: snooze check (prefs.go IsSnoozed). ------------
	snoozed, err := repo.IsSnoozed(ctx, tenantID.String(), userID.String(), "document.shared")
	require.NoError(t, err, "IsSnoozed must not error")
	assert.True(t, snoozed,
		"IsSnoozed must see the active '*' snooze; false means the raw-pool read "+
			"fail-closed and the snooze silently no-oped")

	// --- Flagged read 3: DND lookup (prefs.go GetDND). ------------------
	dnd, err := repo.GetDND(ctx, tenantID.String(), userID.String())
	require.NoError(t, err, "GetDND must not error")
	if assert.NotNil(t, dnd,
		"GetDND must return the seeded DND window; nil means the raw-pool read "+
			"fail-closed (ErrNoRows) and DND silently no-oped") {
		assert.Equal(t, "22:00", dnd.DNDStart)
		assert.Equal(t, "07:00", dnd.DNDEnd)
	}
}
