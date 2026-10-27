//go:build integration
// +build integration

// SetDocumentLifecycle against a REAL Postgres running the document
// service's migration chain (DEF-02). The activity used to run a bare
// `UPDATE documents SET lifecycle_state = $1`: no legal-hold check, no
// state-machine check, no outbox event — so an approval/review workflow
// could flip a held document to active/draft and nothing downstream
// (search, audit, webhooks) heard about it. These cases pin the fixed
// contract:
//
//   - held (binding-table hold or the in-band legal_hold state) → document
//     unchanged, no outbox row, non-retryable "LegalHold" error;
//   - a transition the document state machine does not allow → unchanged,
//     non-retryable "InvalidLifecycleTransition" error;
//   - a valid transition → state changed + exactly one
//     dms.document.state_changed.v1 outbox row per state-machine hop, in
//     the document service's payload shape;
//   - no live document row → non-retryable "DocumentNotFound" error.
//
// The real chain (not standalone DDL) is used because the CHECK on
// documents.lifecycle_state, the legal-hold tables and the outbox columns
// are exactly what the activity has to agree with.
package activities

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

const documentMigrationsDir = "../../../document/migrations"

type lifecycleFixture struct {
	a      *Activities
	ctx    context.Context
	tenant uuid.UUID
	user   uuid.UUID
	ws     uuid.UUID
	folder uuid.UUID
}

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	t.Cleanup(cancel)

	dsn, cleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// document 000021+ ALTER document_entities, which the intelligence
	// service's migration set owns; pre-create it so the single-service
	// chain completes (same workaround as the document/task fixtures).
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id    UUID NOT NULL,
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id   UUID NOT NULL,
			document_id  UUID NOT NULL,
			entity_type  TEXT NOT NULL,
			entity_value TEXT NOT NULL,
			start_offset INT  NOT NULL DEFAULT 0,
			end_offset   INT  NOT NULL DEFAULT 0,
			confidence   REAL NOT NULL DEFAULT 0,
			is_pii       BOOLEAN NOT NULL DEFAULT false,
			detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		)`)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(dsn, documentMigrationsDir))

	f := &lifecycleFixture{
		a:      &Activities{Pool: pool, Outbox: database.NewOutboxRepository(), Log: zerolog.Nop()},
		ctx:    ctx,
		tenant: uuid.Must(uuid.NewV7()),
		user:   uuid.Must(uuid.NewV7()),
		ws:     uuid.Must(uuid.NewV7()),
		folder: uuid.Must(uuid.NewV7()),
	}
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, 'Org', $2)`,
		f.tenant, "t-"+f.tenant.String())
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Admin', 'admin')`, f.tenant, f.user, "u-"+f.user.String()+"@test.local")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, f.tenant, f.ws)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)`, f.tenant, f.folder, f.ws)
	require.NoError(t, err)
	return f
}

// seedDoc inserts a live document in the given lifecycle state.
func (f *lifecycleFixture) seedDoc(t *testing.T, state string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	_, err := f.a.Pool.Exec(f.ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, lifecycle_state,
		                       under_legal_hold)
		VALUES ($1, $2, $3, $4, 'Contract', $5, $5 = 'legal_hold')`,
		f.tenant, id, f.ws, f.folder, state)
	require.NoError(t, err)
	return id
}

// bindHold places an active compliance-API legal hold on doc: a
// legal_holds row + legal_hold_documents binding, lifecycle_state left
// as-is (compliance holds never change it).
func (f *lifecycleFixture) bindHold(t *testing.T, doc uuid.UUID, prevState string) {
	t.Helper()
	hold := uuid.Must(uuid.NewV7())
	_, err := f.a.Pool.Exec(f.ctx, `
		INSERT INTO legal_holds (tenant_id, id, name, applied_by, is_active)
		VALUES ($1, $2, 'Matter 42', $3, true)`, f.tenant, hold, f.user)
	require.NoError(t, err)
	_, err = f.a.Pool.Exec(f.ctx, `
		INSERT INTO legal_hold_documents (tenant_id, hold_id, document_id, previous_lifecycle_state)
		VALUES ($1, $2, $3, $4)`, f.tenant, hold, doc, prevState)
	require.NoError(t, err)
}

// openHoldTx starts the compliance hold path exactly as HoldsService.Create
// runs it (services/document/internal/compliance/holds.go:136-170):
// INSERT the legal_holds row, share-lock the document (SELECT … FOR
// SHARE), INSERT the binding — and leaves the transaction OPEN, holding
// the share lock. The returned func commits it.
func (f *lifecycleFixture) openHoldTx(t *testing.T, doc uuid.UUID) (commit func()) {
	t.Helper()
	tx, err := f.a.Pool.Begin(f.ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	hold := uuid.Must(uuid.NewV7())
	_, err = tx.Exec(f.ctx, `
		INSERT INTO legal_holds (tenant_id, id, name, applied_by, is_active)
		VALUES ($1, $2, 'Matter 7', $3, true)`, f.tenant, hold, f.user)
	require.NoError(t, err)
	var prev string
	require.NoError(t, tx.QueryRow(f.ctx, `
		SELECT lifecycle_state FROM documents
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		   FOR SHARE`, f.tenant, doc).Scan(&prev))
	_, err = tx.Exec(f.ctx, `
		INSERT INTO legal_hold_documents (tenant_id, hold_id, document_id, previous_lifecycle_state)
		VALUES ($1, $2, $3, $4)`, f.tenant, hold, doc, prev)
	require.NoError(t, err)
	return func() { require.NoError(t, tx.Commit(f.ctx)) }
}

// waitForLockWaiter blocks until some backend is waiting on a row lock —
// i.e. the activity under test has issued its statement and is queued
// behind the hold transaction's share lock. Each fixture has its own
// Postgres, so any lock waiter is the activity.
func (f *lifecycleFixture) waitForLockWaiter(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		var n int
		require.NoError(t, f.a.Pool.QueryRow(f.ctx, `
			SELECT count(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&n))
		return n > 0
	}, 30*time.Second, 10*time.Millisecond, "the activity never blocked on the document row lock")
}

func (f *lifecycleFixture) state(t *testing.T, doc uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, f.a.Pool.QueryRow(f.ctx,
		`SELECT lifecycle_state FROM documents WHERE tenant_id = $1 AND id = $2`, f.tenant, doc).Scan(&s))
	return s
}

type outboxRow struct {
	EventType     string
	AggregateType string
	AggregateID   uuid.UUID
	Payload       map[string]any
}

func (f *lifecycleFixture) outbox(t *testing.T, doc uuid.UUID) []outboxRow {
	t.Helper()
	rows, err := f.a.Pool.Query(f.ctx, `
		SELECT event_type, aggregate_type, aggregate_id, payload
		  FROM outbox WHERE tenant_id = $1 AND aggregate_id = $2
		 ORDER BY created_at, id`, f.tenant, doc)
	require.NoError(t, err)
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		var raw []byte
		require.NoError(t, rows.Scan(&r.EventType, &r.AggregateType, &r.AggregateID, &raw))
		require.NoError(t, json.Unmarshal(raw, &r.Payload))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

// requireNonRetryable asserts err is a non-retryable Temporal application
// error of the given type — so the activity is not retried and the
// calling workflow can branch on the type. The assertion is on the
// returned error's own type, not errors.As: the SDK's failure converter
// type-switches on the top-level error, so an ApplicationError wrapped by
// fmt.Errorf would reach the server as a plain, retryable failure.
func requireNonRetryable(t *testing.T, err error, wantType string) {
	t.Helper()
	require.Error(t, err)
	appErr, ok := err.(*temporal.ApplicationError) //nolint:errorlint // see above: wrapping is the bug
	require.True(t, ok, "want an unwrapped *temporal.ApplicationError, got %T: %v", err, err)
	require.Equal(t, wantType, appErr.Type())
	require.True(t, appErr.NonRetryable(), "%s must be non-retryable", wantType)
}

func TestSetDocumentLifecycle_LegalHoldBinding_Unchanged(t *testing.T) {
	f := newLifecycleFixture(t)
	doc := f.seedDoc(t, "in_review")
	f.bindHold(t, doc, "in_review")

	err := f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), "active")

	requireNonRetryable(t, err, "LegalHold")
	require.Equal(t, "in_review", f.state(t, doc), "a held document must not change state")
	require.Empty(t, f.outbox(t, doc), "a refused change must not emit an event")
}

func TestSetDocumentLifecycle_LegalHoldState_Unchanged(t *testing.T) {
	f := newLifecycleFixture(t)
	doc := f.seedDoc(t, "legal_hold")

	err := f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), "draft")

	requireNonRetryable(t, err, "LegalHold")
	require.Equal(t, "legal_hold", f.state(t, doc))
	require.Empty(t, f.outbox(t, doc))
}

func TestSetDocumentLifecycle_InvalidTransition_Unchanged(t *testing.T) {
	cases := []struct{ from, target string }{
		{"active", "draft"},    // a workflow rejection must not demote an active document
		{"archived", "active"}, // nor resurrect an archived one
		{"superseded", "active"},
		{"disposed", "active"},
		{"in_review", "archived"}, // a target the workflows never set
	}
	f := newLifecycleFixture(t)
	for _, tc := range cases {
		t.Run(tc.from+"->"+tc.target, func(t *testing.T) {
			doc := f.seedDoc(t, tc.from)

			err := f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), tc.target)

			requireNonRetryable(t, err, "InvalidLifecycleTransition")
			require.Equal(t, tc.from, f.state(t, doc))
			require.Empty(t, f.outbox(t, doc))
		})
	}
}

func TestSetDocumentLifecycle_ValidTransition_OneOutboxRow(t *testing.T) {
	cases := []struct{ from, target, action string }{
		{"in_review", "active", "approve"},
		{"in_review", "draft", "reject"},
	}
	f := newLifecycleFixture(t)
	for _, tc := range cases {
		t.Run(tc.from+"->"+tc.target, func(t *testing.T) {
			doc := f.seedDoc(t, tc.from)

			require.NoError(t, f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), tc.target))

			require.Equal(t, tc.target, f.state(t, doc))
			rows := f.outbox(t, doc)
			require.Len(t, rows, 1, "exactly one state_changed event per transition")
			r := rows[0]
			require.Equal(t, "dms.document.state_changed.v1", r.EventType)
			require.Equal(t, "document", r.AggregateType)
			require.Equal(t, doc, r.AggregateID)
			require.Equal(t, doc.String(), r.Payload["document_id"])
			require.Equal(t, tc.from, r.Payload["from_state"])
			require.Equal(t, tc.target, r.Payload["to_state"])
			require.Equal(t, tc.action, r.Payload["action"])
			require.NotEmpty(t, r.Payload["changed_by"])
		})
	}
}

// TestSetDocumentLifecycle_DraftApproved_TwoHops covers the live path:
// ApprovalWorkflow is started on a draft document (nothing submits it for
// review first), so its approval reaches active through the two
// state-machine hops submit_for_review + approve — each recorded.
func TestSetDocumentLifecycle_DraftApproved_TwoHops(t *testing.T) {
	f := newLifecycleFixture(t)
	doc := f.seedDoc(t, "draft")

	require.NoError(t, f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), "active"))

	require.Equal(t, "active", f.state(t, doc))
	rows := f.outbox(t, doc)
	require.Len(t, rows, 2)
	require.Equal(t, "draft", rows[0].Payload["from_state"])
	require.Equal(t, "in_review", rows[0].Payload["to_state"])
	require.Equal(t, "submit_for_review", rows[0].Payload["action"])
	require.Equal(t, "in_review", rows[1].Payload["from_state"])
	require.Equal(t, "active", rows[1].Payload["to_state"])
	require.Equal(t, "approve", rows[1].Payload["action"])
	for _, r := range rows {
		require.Equal(t, "dms.document.state_changed.v1", r.EventType)
	}
}

// TestSetDocumentLifecycle_AlreadyInTarget_NoOp: a rejection of a draft
// document (the live case) and a retried activity whose first attempt
// committed both find the document already in the target state — success,
// nothing written.
func TestSetDocumentLifecycle_AlreadyInTarget_NoOp(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, s := range []string{"draft", "active"} {
		doc := f.seedDoc(t, s)
		require.NoError(t, f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), s))
		require.Equal(t, s, f.state(t, doc))
		require.Empty(t, f.outbox(t, doc))
	}
}

func TestSetDocumentLifecycle_NotFound(t *testing.T) {
	f := newLifecycleFixture(t)

	err := f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), uuid.Must(uuid.NewV7()).String(), "active")
	requireNonRetryable(t, err, "DocumentNotFound")

	// A soft-deleted document is not a transition target either.
	doc := f.seedDoc(t, "in_review")
	_, err = f.a.Pool.Exec(f.ctx, `UPDATE documents SET deleted_at = now() WHERE tenant_id = $1 AND id = $2`, f.tenant, doc)
	require.NoError(t, err)
	err = f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), "active")
	requireNonRetryable(t, err, "DocumentNotFound")
	require.Equal(t, "in_review", f.state(t, doc))
	require.Empty(t, f.outbox(t, doc))
}

// TestSetDocumentLifecycle_HoldCommittedWhileWaitingOnLock_Refused is the
// I-1 race (review of DEF-02): a compliance hold share-locks the document
// and inserts its binding; the activity starts and waits for the row
// lock; the hold commits; the activity gets the lock. Reading the binding
// in the SAME statement that took the lock used that statement's
// snapshot — taken before the wait — so the committed hold was invisible
// and the held document moved in_review -> active. The hold must win: no
// change, no event, a non-retryable LegalHold.
//
// Deterministic: the hold transaction is open before the activity starts,
// the test waits until Postgres reports the activity blocked on the lock,
// and only then commits the hold.
func TestSetDocumentLifecycle_HoldCommittedWhileWaitingOnLock_Refused(t *testing.T) {
	f := newLifecycleFixture(t)
	doc := f.seedDoc(t, "in_review")

	commitHold := f.openHoldTx(t, doc)

	done := make(chan error, 1)
	go func() { done <- f.a.SetDocumentLifecycle(f.ctx, f.tenant.String(), doc.String(), "active") }()

	f.waitForLockWaiter(t)
	commitHold()

	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("activity did not finish after the hold committed")
	}

	requireNonRetryable(t, err, "LegalHold")
	require.Equal(t, "in_review", f.state(t, doc), "a hold committed while the activity waited must stop the change")
	require.Empty(t, f.outbox(t, doc), "a refused change must not emit an event")
}
