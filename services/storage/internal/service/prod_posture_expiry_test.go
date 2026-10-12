//go:build integration && prodposture
// +build integration,prodposture

// L-6 — the upload-expiry sweep under the prod NOBYPASSRLS role.
//
// upload_sessions is FORCE ROW LEVEL SECURITY. MarkExpired ran one
// cross-tenant UPDATE on the raw pool with no app.current_tenant, which
// under dms_app matches zero rows — and nothing called it anyway, so
// abandoned and crashed sessions stayed initiated/scanning forever. The
// sweep now runs per tenant (each under its own tenant context) on a
// 5-minute ticker.
package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

const expiryFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	deleted_at TIMESTAMPTZ
);
CREATE TABLE upload_sessions (
	tenant_id        UUID NOT NULL REFERENCES organizations(id),
	id               UUID NOT NULL DEFAULT gen_random_uuid(),
	document_id      UUID,
	filename         TEXT NOT NULL,
	total_size       BIGINT NOT NULL,
	mime_type        TEXT,
	upload_type      TEXT NOT NULL CHECK (upload_type IN ('single', 'multipart', 'tus')),
	storage_region   TEXT NOT NULL DEFAULT 'us-east-1',
	s3_upload_id     TEXT,
	status           TEXT NOT NULL DEFAULT 'initiated'
	                      CHECK (status IN ('initiated', 'uploading', 'scanning',
	                                        'completed', 'failed', 'quarantined')),
	parts_completed  INT NOT NULL DEFAULT 0,
	parts_total      INT,
	created_by       UUID,
	created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at     TIMESTAMPTZ,
	expires_at       TIMESTAMPTZ NOT NULL,
	content_blob_id  UUID,
	PRIMARY KEY (tenant_id, id)
);
ALTER TABLE upload_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE upload_sessions FORCE  ROW LEVEL SECURITY;
CREATE POLICY upload_sessions_tenant_isolation ON upload_sessions
	USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
CREATE POLICY upload_sessions_tenant_isolation_insert ON upload_sessions
	FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
`

func TestProdPosture_MarkExpiredSweeps(t *testing.T) {
	testutil.AssertProdPosture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, expiryFixtureDDL)
	require.NoError(t, err)
	up, err := os.ReadFile(filepath.Join("..", "..", "..", "document", "migrations", migration000107))
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(up))
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `
		GRANT SELECT, INSERT, UPDATE, DELETE ON upload_sessions TO dms_app;
		GRANT SELECT ON organizations TO dms_app;`)
	require.NoError(t, err)

	tenantA, tenantB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1), ($2)`, tenantA, tenantB)
	require.NoError(t, err)

	now := time.Now().UTC()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	type row struct {
		tenant uuid.UUID
		status string
		expiry time.Time
		want   string
	}
	rows := []row{
		{tenantA, "initiated", past, "failed"},
		{tenantA, "scanning", past, "failed"}, // crashed claimer
		{tenantA, "initiated", future, "initiated"},
		{tenantA, "completed", past, "completed"},
		{tenantB, "uploading", past, "failed"},
		{tenantB, "scanning", past, "failed"},
		{tenantB, "quarantined", past, "quarantined"},
		{tenantB, "scanning", future, "scanning"},
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = uuid.Must(uuid.NewV7())
		_, err := db.Super.Exec(ctx, `
			INSERT INTO upload_sessions (tenant_id, id, filename, total_size, upload_type, status, expires_at)
			VALUES ($1, $2, 'f.txt', 1, 'single', $3, $4)`, r.tenant, ids[i], r.status, r.expiry)
		require.NoError(t, err)
	}

	svc := New(Config{Pool: db.App, Repos: repository.New(db.App), Logger: zerolog.Nop()})
	n, err := svc.SweepExpiredUploads(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 4, n, "both tenants' expired in-flight sessions are swept under dms_app")

	for i, r := range rows {
		var got string
		require.NoError(t, db.Super.QueryRow(ctx,
			`SELECT status FROM upload_sessions WHERE id = $1`, ids[i]).Scan(&got))
		require.Equal(t, r.want, got, "row %d (%s, %s)", i, r.status, r.expiry.Sub(now).Round(time.Minute))
	}

	// The sweep is a pure per-tenant UPDATE: running it again is a no-op.
	n, err = svc.SweepExpiredUploads(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}
