//go:build integration

// D-16 / DEC-13: the retention sweep is a system flow, not a user. It used to
// replay UpdateLifecycle as a synthetic user with role "system", which no
// policy rule allows — every archive/dispose was denied while the sweep
// reported success. The sweep now marks its context with an unexported key
// that only SweepRetention sets, and UpdateLifecycle skips the policy check
// for that marker (archive/dispose only). No role is faked, and a real user
// gets no such bypass.
package service_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// seedRetentionPolicy inserts an active policy scoped to wsID.
func seedRetentionPolicy(t *testing.T, h *harness, wsID uuid.UUID, name, action string, days int) {
	t.Helper()
	_, err := h.pool.Exec(h.ctx, `
		INSERT INTO retention_policies (tenant_id, name, workspace_filter, retain_days, then_action, is_active)
		VALUES ($1, $2, $3, $4, $5, true)`, h.tenant, name, wsID, days, action)
	require.NoError(t, err)
}

func backdate(t *testing.T, h *harness, docID uuid.UUID, days int) {
	t.Helper()
	_, err := h.pool.Exec(h.ctx, `
		UPDATE documents SET created_at = now() - make_interval(days => $3)
		 WHERE tenant_id = $1 AND id = $2`, h.tenant, docID, days)
	require.NoError(t, err)
}

func lifecycleOf(t *testing.T, h *harness, docID uuid.UUID) string {
	t.Helper()
	var s string
	require.NoError(t, h.pool.QueryRow(h.ctx,
		`SELECT lifecycle_state FROM documents WHERE tenant_id = $1 AND id = $2`, h.tenant, docID).Scan(&s))
	return s
}

func setState(t *testing.T, h *harness, docID uuid.UUID, state model.LifecycleState) {
	t.Helper()
	require.NoError(t, database.WithTenantTx(h.ctx, h.repos.Pool, h.tenant, func(tx pgx.Tx) error {
		return h.repos.Documents.UpdateLifecycleState(h.ctx, tx, h.tenant, docID, state)
	}))
}

func TestRetentionSweep_ArchivesUnderRealPolicy(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Retained")
	old := mustDocIn(t, h, h.svc, wsID, f.ID, "old")
	held := mustDocIn(t, h, h.svc, wsID, f.ID, "held")
	fresh := mustDocIn(t, h, h.svc, wsID, f.ID, "fresh")
	for _, d := range []*model.Document{old, held, fresh} {
		setState(t, h, d.ID, model.StateActive)
	}
	backdate(t, h, old.ID, 400)
	backdate(t, h, held.ID, 400)
	seedRetentionPolicy(t, h, wsID, "one-year-archive", "archive", 365)

	// held is under a compliance-API hold (binding row only; state stays active).
	complianceHold(t, h, held.ID)

	// The production policy allows nothing to a principal it does not know —
	// model it as deny-all. The sweep must still act; a hold still wins.
	strict := serviceWith(h, stubPolicy{allow: false})
	res, err := strict.SweepRetention(context.Background(), h.tenant, 50)
	require.NoError(t, err)
	require.Equal(t, 1, res.Archived, "the sweep must archive under a policy that grants the system nothing")
	require.Equal(t, 0, res.Errors)
	require.Equal(t, 1, res.SkippedByHold)
	require.Equal(t, string(model.StateRetained), lifecycleOf(t, h, old.ID), "active --archive--> retained")
	require.Equal(t, string(model.StateActive), lifecycleOf(t, h, held.ID), "a hold still wins")
	require.Equal(t, string(model.StateActive), lifecycleOf(t, h, fresh.ID), "inside the window: untouched")

	// The bypass is the sweep's alone: a real user under the same policy is
	// still refused, whatever role their session claims.
	_, err = strict.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: fresh.ID, Action: model.ActionArchive, Reason: "manual",
	})
	require.ErrorIs(t, err, vdmserr.ErrForbidden)
	archived := mustDocIn(t, h, h.svc, wsID, f.ID, "archived")
	setState(t, h, archived.ID, model.StateArchived)
	_, err = strict.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
		DocumentID: archived.ID, Action: model.ActionDispose, Reason: "manual",
	})
	require.ErrorIs(t, err, vdmserr.ErrForbidden)
}
