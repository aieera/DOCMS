//go:build integration && prodposture
// +build integration,prodposture

// N-3: a re-dropped file whose earlier ingest failed must be ingested, and
// concurrent sweeps of the same folder must ingest each file exactly once
// without moving a file out from under an in-flight ingest. Runs against
// the schema of record (document migration 000042) under NOBYPASSRLS.
package intake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

type intakeFixture struct {
	db     *testutil.ProdPostureDB
	svc    *Service
	folder Folder
	inbox  string
	tenant uuid.UUID
}

func newIntakeFixture(ctx context.Context, t *testing.T, ing ingester) *intakeFixture {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	ddl, err := os.ReadFile("../../../document/migrations/000042_intake_drop_folders.up.sql")
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);`)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(ddl))
	require.NoError(t, err, "document 000042 (schema of record) must apply")
	_, err = db.Super.Exec(ctx, `GRANT SELECT ON organizations TO dms_app`)
	require.NoError(t, err)

	tenant := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	root := t.TempDir()
	inbox := filepath.Join(root, tenant.String(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o755))

	svc := New(db.App, nil, zerolog.Nop())
	svc.root = root
	svc.settleDelay = 10 * time.Millisecond
	svc.ingest = ing

	folderID := uuid.New()
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO intake_drop_folders
			    (tenant_id, id, label, host_path, target_workspace_id, target_folder_id, created_by)
			VALUES ($1, $2, 'inbox', $3, $4, $5, $6)`,
			tenant, folderID, inbox, uuid.New(), uuid.New(), uuid.New())
		return err
	}))
	f, err := svc.getFolder(ctx, tenant.String(), folderID.String())
	require.NoError(t, err)
	require.NotNil(t, f)
	require.NoError(t, ensureSubdirs(*f))
	return &intakeFixture{db: db, svc: svc, folder: *f, inbox: inbox, tenant: tenant}
}

func (fx *intakeFixture) rowStatus(ctx context.Context, t *testing.T, sha string) string {
	t.Helper()
	var st string
	require.NoError(t, database.WithTenantTx(ctx, fx.db.App, fx.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT ingest_status FROM intake_ingested_files WHERE tenant_id = $1 AND sha256 = $2`,
			fx.tenant, sha).Scan(&st)
	}))
	return st
}

// scriptedIngester fails the first N calls, then succeeds.
type scriptedIngester struct {
	mu       sync.Mutex
	failures int
	calls    int
}

func (s *scriptedIngester) IngestFile(context.Context, string, string, string, string, string, string, string, []byte, map[string]any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failures {
		return "", errors.New("storage blip")
	}
	return uuid.NewString(), nil
}

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func TestIntake_RedropAfterFailureIngests(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	ing := &scriptedIngester{failures: 1}
	fx := newIntakeFixture(ctx, t, ing)
	body := []byte("%PDF-1.4 invoice 42")
	sha := shaHex(body)
	p := filepath.Join(fx.inbox, "invoice.pdf")

	require.NoError(t, os.WriteFile(p, body, 0o644))
	fx.svc.handleFile(ctx, fx.folder, p)
	require.Equal(t, 1, ing.calls)
	require.Equal(t, "failed", fx.rowStatus(ctx, t, sha))
	require.FileExists(t, filepath.Join(fx.inbox, "quarantine", "invoice.pdf"))

	// Re-drop after the operator fixed the cause: must ingest, not be
	// filed as a duplicate of the failed attempt.
	require.NoError(t, os.WriteFile(p, body, 0o644))
	fx.svc.handleFile(ctx, fx.folder, p)
	require.Equal(t, 2, ing.calls, "re-dropped file after a failure must be ingested")
	require.Equal(t, "ingested", fx.rowStatus(ctx, t, sha))
	require.FileExists(t, filepath.Join(fx.inbox, "processed", "invoice.pdf"))
	require.NoFileExists(t, p)

	// A true duplicate of ingested content is still filed without ingest.
	dup := filepath.Join(fx.inbox, "copy.pdf")
	require.NoError(t, os.WriteFile(dup, body, 0o644))
	fx.svc.handleFile(ctx, fx.folder, dup)
	require.Equal(t, 2, ing.calls, "duplicate of ingested content must not be re-ingested")
	require.FileExists(t, filepath.Join(fx.inbox, "processed", "copy.pdf"))
}

// slowIngester takes a while and records, during each call, whether the
// file it is ingesting has already been moved to processed/ by someone else.
type slowIngester struct {
	mu        sync.Mutex
	processed string
	perName   map[string]int
	violation []string
}

func (s *slowIngester) IngestFile(_ context.Context, _, _, _, _, _ string, filename, _ string, _ []byte, _ map[string]any) (string, error) {
	time.Sleep(300 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.perName[filename]++
	if _, err := os.Stat(filepath.Join(s.processed, filename)); err == nil {
		s.violation = append(s.violation, filename)
	}
	return uuid.NewString(), nil
}

func TestIntake_ConcurrentSweepsSingleIngest(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	ing := &slowIngester{perName: map[string]int{}}
	fx := newIntakeFixture(ctx, t, ing)
	ing.processed = filepath.Join(fx.inbox, "processed")
	names := []string{"a.pdf", "b.pdf", "c.pdf"}
	for i, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(fx.inbox, n), []byte{byte('a' + i), 1, 2, 3}, 0o644))
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fx.svc.sweep(ctx, fx.folder) }()
	}
	wg.Wait()

	for _, n := range names {
		require.Equal(t, 1, ing.perName[n], "%s must be ingested exactly once", n)
		require.FileExists(t, filepath.Join(fx.inbox, "processed", n))
		require.NoFileExists(t, filepath.Join(fx.inbox, n))
	}
	require.Empty(t, ing.violation, "a concurrent sweep moved a file to processed/ while it was being ingested")
	entries, err := os.ReadDir(fx.inbox)
	require.NoError(t, err)
	for _, e := range entries {
		require.True(t, e.IsDir(), "leftover in inbox: %s", e.Name())
	}
}
