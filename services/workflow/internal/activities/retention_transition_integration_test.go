//go:build integration
// +build integration

// RetentionTransition against a REAL Postgres running the document
// service's migration chain (review finding I-3, the retention twin of
// DEF-02). The retention cron checked holds in one activity
// (DocumentOnLegalHold) and then ran RetentionTransition — a bare
// `UPDATE documents SET lifecycle_state = $1` — in another, seconds apart:
// a hold placed in between (binding or in-band) was archived or disposed
// over, and any state could be overwritten. RetentionTransition now locks
// the row, re-checks the hold after the lock in the same transaction and
// walks the document state machine. Reuses the lifecycle fixture
// (set_document_lifecycle_integration_test.go).
package activities

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const subjectRetentionArchived = "dms.retention.archived.v1"

func TestRetentionTransition_HoldCommittedWhileWaitingOnLock_Refused(t *testing.T) {
	f := newLifecycleFixture(t)
	doc := f.seedDoc(t, "active")

	commitHold := f.openHoldTx(t, doc)

	done := make(chan error, 1)
	go func() {
		done <- f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), "archived", subjectRetentionArchived)
	}()

	f.waitForLockWaiter(t)
	commitHold()

	var err error
	select {
	case err = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("RetentionTransition did not finish after the hold committed")
	}

	requireNonRetryable(t, err, "LegalHold")
	require.Equal(t, "active", f.state(t, doc), "a hold committed while the transition waited must stop it")
	require.Empty(t, f.outbox(t, doc))
}

func TestRetentionTransition_Held_Unchanged(t *testing.T) {
	f := newLifecycleFixture(t)

	t.Run("binding", func(t *testing.T) {
		doc := f.seedDoc(t, "retained")
		f.bindHold(t, doc, "retained")
		err := f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), "archived", subjectRetentionArchived)
		requireNonRetryable(t, err, "LegalHold")
		require.Equal(t, "retained", f.state(t, doc))
		require.Empty(t, f.outbox(t, doc))
	})
	t.Run("in-band state", func(t *testing.T) {
		doc := f.seedDoc(t, "legal_hold")
		err := f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), "archived", subjectRetentionArchived)
		requireNonRetryable(t, err, "LegalHold")
		require.Equal(t, "legal_hold", f.state(t, doc))
		require.Empty(t, f.outbox(t, doc))
	})
}

func TestRetentionTransition_InvalidTransition_Unchanged(t *testing.T) {
	cases := []struct{ from, target string }{
		{"draft", "archived"},
		{"in_review", "archived"},
		{"active", "disposed"}, // dispose only from archived
		{"disposed", "archived"},
		{"active", "active"}, // not a retention target
	}
	f := newLifecycleFixture(t)
	for _, tc := range cases {
		t.Run(tc.from+"->"+tc.target, func(t *testing.T) {
			doc := f.seedDoc(t, tc.from)
			err := f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), tc.target, subjectRetentionArchived)
			requireNonRetryable(t, err, "InvalidLifecycleTransition")
			require.Equal(t, tc.from, f.state(t, doc))
			require.Empty(t, f.outbox(t, doc))
		})
	}
}

// Valid transitions still apply and still emit exactly the retention
// event the workflow names (no new event kinds on this path).
func TestRetentionTransition_Valid_OneRetentionEvent(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, from := range []string{"active", "retained"} {
		t.Run(from, func(t *testing.T) {
			doc := f.seedDoc(t, from)
			require.NoError(t, f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), "archived", subjectRetentionArchived))
			require.Equal(t, "archived", f.state(t, doc))
			rows := f.outbox(t, doc)
			require.Len(t, rows, 1)
			require.Equal(t, subjectRetentionArchived, rows[0].EventType)
			data, _ := rows[0].Payload["data"].(map[string]any)
			require.Equal(t, "archived", data["new_state"])
		})
	}

	// Retry after a committed first attempt: already archived is a no-op.
	doc := f.seedDoc(t, "archived")
	require.NoError(t, f.a.RetentionTransition(f.ctx, f.tenant.String(), doc.String(), "archived", subjectRetentionArchived))
	require.Equal(t, "archived", f.state(t, doc))
	require.Empty(t, f.outbox(t, doc))
}

func TestRetentionTransition_NotFound(t *testing.T) {
	f := newLifecycleFixture(t)
	err := f.a.RetentionTransition(f.ctx, f.tenant.String(), uuid.Must(uuid.NewV7()).String(), "archived", subjectRetentionArchived)
	requireNonRetryable(t, err, "DocumentNotFound")
}
