//go:build integration

// M-4 / M-5: read-modify-write paths on a document must not lose a
// concurrent writer's change. Each test parks the first writer inside its
// permission check (gatedPolicy) — the window between its read and its write
// — runs a second writer to completion (or until it blocks on the row), then
// lets the first one finish.
package service_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// awaitParked waits until the gated caller is inside its permission check.
func awaitParked(t *testing.T, g *gatedPolicy) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("first writer never reached its permission check")
	}
}

// runConcurrently starts fn and returns once it has finished (blocked=false)
// or is blocked on a lock it did not find already contended
// (blocked=true), plus a channel with its result. Neither within 10s fails
// the test, so a caller asserting either outcome really observed it.
func runConcurrently(t *testing.T, h *harness, fn func() error) (<-chan error, bool) {
	t.Helper()
	before := lockWaiters(t, h)
	done := make(chan error, 1)
	go func() { done <- fn() }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(done) > 0 {
			return done, false
		}
		if lockWaiters(t, h) > before {
			return done, true
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("concurrent writer neither finished nor blocked on a lock")
	return done, false
}

func recv(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("writer did not finish")
		return nil
	}
}

func TestUpdateDocument_DoesNotRevertConcurrentVersion(t *testing.T) {
	h := newHarness(t, true)
	gate := newGatedPolicy()
	defer gate.open()
	gated := serviceWith(h, gate)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Race")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "before")
	mustLinkVersion(t, h, h.svc, doc.ID, seedUploadedBlob(t, h, h.user, "us-east-1"))

	// Writer 1: a title edit, parked between its read and its write.
	gate.armed.Store(true)
	newTitle := "after"
	edit := make(chan error, 1)
	go func() {
		_, err := gated.UpdateDocument(h.ctx, &service.UpdateDocumentInput{DocumentID: doc.ID, Title: &newTitle, UpdatedBy: h.user})
		edit <- err
	}()
	awaitParked(t, gate)

	// Writer 2: a new upload lands meanwhile.
	blob2 := seedUploadedBlob(t, h, h.user, "us-east-1")
	var v2 *model.Version
	upload, blocked := runConcurrently(t, h, func() error {
		var err error
		v2, err = h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blob2.ID})
		return err
	})
	require.True(t, blocked, "the upload must wait for the parked edit's row lock")

	gate.open()
	require.NoError(t, recv(t, edit))
	require.NoError(t, recv(t, upload))

	got, _, err := h.svc.GetDocument(h.ctx, doc.ID)
	require.NoError(t, err)
	require.Equal(t, "after", got.Title)
	require.NotNil(t, got.CurrentVersionID)
	require.Equal(t, v2.ID, *got.CurrentVersionID, "the title edit must not revert the head to v1")
	require.Equal(t, v2.SHA256Hash, got.SHA256Hash)
	require.Equal(t, int32(2), got.VersionCount)
}

func TestUpdateLifecycle_ConcurrentTransitionConflicts(t *testing.T) {
	h := newHarness(t, true)
	gate := newGatedPolicy()
	defer gate.open()
	gated := serviceWith(h, gate)
	doc := mustCreateDocumentWith(t, h, model.StateActive)

	// Writer 1: archive, parked in its permission check after locking the
	// row and reading state=active.
	gate.armed.Store(true)
	archive := make(chan error, 1)
	go func() {
		_, err := gated.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{DocumentID: doc.ID, Action: model.ActionArchive})
		archive <- err
	}()
	awaitParked(t, gate)

	// Writer 2: apply_hold from the same state waits on the row lock
	// instead of racing (M-5: the race used to end archived with
	// under_legal_hold=false).
	held, blocked := runConcurrently(t, h, func() error {
		_, err := h.svc.UpdateLifecycle(h.ctx, &service.UpdateLifecycleInput{
			DocumentID: doc.ID, Action: model.ActionApplyHold, HoldName: "Race matter", Reason: "litigation",
		})
		return err
	})
	require.True(t, blocked, "the hold must wait for the in-flight archive")

	gate.open()
	require.NoError(t, recv(t, archive))
	require.NoError(t, recv(t, held), "apply_hold then runs on the state the archive committed")

	got, _, err := h.svc.GetDocument(h.ctx, doc.ID)
	require.NoError(t, err)
	require.Equal(t, model.StateLegalHold, got.LifecycleState)
	require.Equal(t, 1, countRows(t, h,
		`SELECT count(*) FROM documents WHERE tenant_id = $1 AND id = $2 AND under_legal_hold`, h.tenant, doc.ID))
	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM legal_hold_documents
		 WHERE tenant_id = $1 AND document_id = $2 AND previous_lifecycle_state = 'retained'`, h.tenant, doc.ID),
		"the hold recorded the state it actually froze")

	// The write itself is a compare-and-set: a transition from a state
	// the row is no longer in is a Conflict, never applied.
	err = database.WithTenantTx(h.ctx, h.pool, h.tenant, func(tx pgx.Tx) error {
		return repository.TransitionLifecycleState(h.ctx, tx, h.tenant, doc.ID, model.StateActive, model.StateRetained)
	})
	require.Equal(t, vdmserr.KindConflict, vdmserr.KindOf(err))
}
