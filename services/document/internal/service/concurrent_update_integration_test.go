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
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/model"
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

// runConcurrently starts fn and returns once it has finished or is blocked
// on a row lock (whichever comes first), plus a channel with its result.
func runConcurrently(t *testing.T, h *harness, fn func() error) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(done) > 0 {
			return done
		}
		if waitForLockWaiter(t, h, 50*time.Millisecond) {
			return done
		}
	}
	return done
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
	upload := runConcurrently(t, h, func() error {
		var err error
		v2, err = h.svc.CreateVersion(h.ctx, &service.CreateVersionInput{DocumentID: doc.ID, ContentBlobID: blob2.ID})
		return err
	})

	close(gate.release)
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

