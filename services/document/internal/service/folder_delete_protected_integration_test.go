//go:build integration

// M-3: DeleteFolder soft-deleted every document in the subtree, including
// held, record-declared and WORM-locked ones — held documents then sat in
// the trash where members could not restore them and the purge paths owned
// them. The delete must refuse (409) and name the documents in the way.
package service_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func TestDeleteFolder_HeldDocument409(t *testing.T) {
	h := newHarness(t, true)
	root, docs := mustCreateTree(t, h) // one doc in root, one in its child folder
	complianceHold(t, h, docs[1].ID)
	_, err := h.pool.Exec(h.ctx,
		`UPDATE documents SET worm_retain_until = now() + interval '30 days' WHERE tenant_id = $1 AND id = $2`,
		h.tenant, docs[0].ID)
	require.NoError(t, err)

	err = h.svc.DeleteFolder(h.ctx, root.ID)
	require.Error(t, err)
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err), "a refused folder delete is a 409")
	require.ErrorContains(t, err, "2 protected document(s)")
	require.ErrorContains(t, err, `"doc-in-PurgeChild" (legal hold)`)
	require.ErrorContains(t, err, `"doc-in-PurgeRoot" (WORM retention)`)
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND deleted_at IS NOT NULL`, h.tenant), "nothing was trashed")
	require.Zero(t, countRows(t, h,
		`SELECT count(*) FROM folders WHERE tenant_id = $1 AND deleted_at IS NOT NULL`, h.tenant))

	// Once nothing in the tree is protected the cascade goes through.
	_, err = h.pool.Exec(h.ctx, `UPDATE legal_holds SET is_active = false WHERE tenant_id = $1`, h.tenant)
	require.NoError(t, err)
	_, err = h.pool.Exec(h.ctx, `UPDATE documents SET worm_retain_until = NULL WHERE tenant_id = $1`, h.tenant)
	require.NoError(t, err)
	require.NoError(t, h.svc.DeleteFolder(h.ctx, root.ID))
	require.Equal(t, 2, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND deleted_at IS NOT NULL`, h.tenant))
}
