//go:build integration && prodposture
// +build integration,prodposture

// Prod-posture repro: audit repository reads run on the raw pool, so under
// the prod NOBYPASSRLS role the hash-chained audit log is unreadable and
// the chain never links.
//
// BUG (audit-verified, STATE_OF_THE_PROJECT 2026-07-03): the write path in
// this package was fixed (FIX-7: Insert/BatchInsert wrap database.WithTenantTx),
// but the READ paths were not: GetLastHash (repository.go ~line 156), List
// (~:225), ListAll (~:256), ListBySubject (~:284) and AnonymizeSubject
// (~:312) all query the raw *pgxpool.Pool without ever establishing
// app.current_tenant. audit_events carries ENABLE + FORCE ROW LEVEL
// SECURITY keyed on current_setting('app.current_tenant', true)
// (services/document/migrations/000001_initial_schema.up.sql:638-643), so
// under the dms_app (NOBYPASSRLS) role every one of those reads fails
// CLOSED:
//
//   - GET /audit/events returns an empty log even though rows exist, and
//   - IngestEvent's prev-hash lookup (service.go:229 → repo.GetLastHash)
//     finds nothing, so EVERY event is written with previous_hash = ” and
//     the tamper-evidence hash chain never links — silent, unrecoverable
//     integrity loss for every event ingested while running on prod posture.
//
// Dev and the default integration lane connect as a BYPASSRLS superuser,
// which masks all of this. Note the linked RBAC gap from the same audit row
// (audit read endpoints not enforcing an admin role) is currently MASKED by
// this bug — fail-closed RLS hides the data an RBAC bypass would expose —
// so the two must be fixed together or fixing this one alone widens
// exposure.
//
// Tracks: https://github.com/aieera/DOCMS/issues/74
//
// This test drives the REAL repository over the dms_app pool exactly as
// services/audit/cmd/server/main.go constructs it, mirroring the service's
// ingest flow (GetLastHash → chain → Insert) for two events, then reads
// them back through the repo. It asserts the CORRECT post-fix behavior —
// reads return both events and event #2's previous_hash links to event #1
// — so today it FAILS with the RLS fail-closed symptom. It is allow-listed
// in ci/prod-posture-allowlist.txt until the Wave A fix lands; remove the
// allowlist entry when this goes green.
package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // migrate driver
	_ "github.com/golang-migrate/migrate/v4/source/file"       // migrate source
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/repository"
)

func TestProdPosture_AuditRepo(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	// audit_events itself lives in the DOCUMENT service's schema-of-record
	// (migration 000001: partitioned table + ENABLE/FORCE RLS + tenant
	// policies); the audit service's own migrations only ALTER it and add
	// audit_checkpoints / siem_sinks (no FKs beyond 000001's tables). The
	// full document migrations dir can NOT run on a fresh DB (000021
	// depends on document_entities, created by the INTELLIGENCE service's
	// migrations), so apply exactly document 000001, then the audit
	// migrations under their own audit_schema_migrations table, exactly as
	// `make migrate-up SERVICE=audit` would.
	db := testutil.NewProdPostureDB(ctx, t, "")
	m, err := migrate.New("file://../../../../services/document/migrations", db.SuperDSN)
	require.NoError(t, err, "init document migrator")
	require.NoError(t, m.Steps(1), "document migration 000001 (audit_events + FORCE RLS)")
	_, _ = m.Close()
	require.NoError(t,
		database.RunServiceMigrations(db.SuperDSN, "../../migrations", "audit"),
		"audit service migrations on top of the document schema-of-record")

	// NewProdPostureDB granted dms_app on ALL TABLES before any of the
	// migrations above ran; re-apply so the tables they created get the
	// same grants migration 000065 gives in prod.
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`)
	require.NoError(t, err, "grant dms_app after migrations")

	// The REAL repository over the NOBYPASSRLS app pool, exactly as
	// services/audit/cmd/server/main.go wires it in production.
	repo := repository.New(db.App)

	tenantID := uuid.Must(uuid.NewV7())
	actorID := uuid.Must(uuid.NewV7()) // Insert casts actor → actor_id::uuid

	// created_at pinned inside the audit_events_2026_07 partition that
	// document migration 000001 creates, so the test doesn't rot when the
	// wall clock leaves the pre-created partition ranges.
	t0 := time.Date(2026, time.July, 1, 10, 0, 0, 0, time.UTC)

	newEvent := func(id string, action string, prev string, at time.Time) *model.AuditEvent {
		return &model.AuditEvent{
			ID:           id,
			TenantID:     tenantID.String(),
			PreviousHash: prev,
			EventHash:    "hash-" + action, // repo persists caller-computed hashes
			Actor:        actorID.String(),
			ActorName:    "prod-posture repro",
			Action:       action,
			Details:      []byte(`{}`),
			SourceEvent:  "dms.test.prodposture.v1",
			CreatedAt:    at,
		}
	}

	// --- Mirror the service ingest flow (service.go IngestEvent) twice. ---

	// Event 1: chain head lookup on an empty log ("" is correct here).
	prev1, err := repo.GetLastHash(ctx, tenantID.String())
	require.NoError(t, err, "GetLastHash on empty log")
	e1 := newEvent(uuid.Must(uuid.NewV7()).String(), "document.viewed", prev1, t0)
	require.NoError(t, repo.Insert(ctx, e1),
		"repo.Insert of event 1 must succeed under prod posture (an RLS "+
			"WITH CHECK rejection here is the same tenant-context bug)")

	// Event 2: the prev-hash lookup that links the chain. Under the bug
	// this fails one of two ways, depending on which pooled connection it
	// lands on: a connection previously used by WithTenantTx has
	// app.current_tenant reverted to '' (not NULL), so the RLS policy's
	// ''::uuid cast errors with SQLSTATE 22P02 `invalid input syntax for
	// type uuid: ""`; a fresh connection has the GUC unset (NULL), the
	// policy matches no rows, and the lookup returns "" — the chain
	// silently never links. Insert with whatever it returned, as prod does.
	prev2, err := repo.GetLastHash(ctx, tenantID.String())
	require.NoError(t, err,
		"GetLastHash before event 2 must not error; a 22P02 uuid-cast error "+
			"means the raw-pool read ran under the FORCE-RLS policy without "+
			"tenant context (app.current_tenant='' left by a pooled connection)")
	e2 := newEvent(uuid.Must(uuid.NewV7()).String(), "document.downloaded", prev2, t0.Add(time.Minute))
	require.NoError(t, repo.Insert(ctx, e2), "repo.Insert of event 2")

	// Sanity: both rows really landed (superuser sees through RLS), so any
	// empty read below is the repo's missing tenant context, not a missing row.
	var seeded int
	require.NoError(t, db.Super.QueryRow(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE tenant_id = $1`, tenantID).Scan(&seeded))
	require.Equal(t, 2, seeded, "both events must exist in audit_events")

	// --- Correct post-fix behavior; today each subtest fails RLS-closed. ---

	t.Run("ListReturnsBothEvents", func(t *testing.T) {
		got, _, err := repo.List(ctx, model.ListFilter{TenantID: tenantID.String()})
		require.NoError(t, err)
		require.Len(t, got, 2,
			"repo.List must return both inserted events; 0 rows means List "+
				"queried the raw pool without app.current_tenant (RLS fail-closed)")
	})

	t.Run("HashChainLinks", func(t *testing.T) {
		require.Equal(t, e1.EventHash, prev2,
			"GetLastHash after event 1 must return event 1's hash so event 2 "+
				"links the chain; \"\" means the prev-hash lookup ran on the raw "+
				"pool and the hash chain never links under prod posture")
		all, err := repo.ListAll(ctx, tenantID.String())
		require.NoError(t, err)
		require.Len(t, all, 2, "repo.ListAll must stream both events for integrity verification")
		require.Equal(t, e1.EventHash, all[1].PreviousHash,
			"persisted event 2 must carry event 1's hash as previous_hash")
	})
}
