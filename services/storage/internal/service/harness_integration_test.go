//go:build integration
// +build integration

// Shared integration harness for the storage service: real Postgres
// (testcontainers), real MinIO, optional real envelope encryption
// (LocalKeyManager), scanner disabled.
package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

// completeFixtureDDL — verbatim shapes of the tables the storage service
// touches as they stood BEFORE document migration 000107 (document
// 000001 + 000003 encrypted_dek + 000004 tenant_keks + 000060
// tenant_upload_policies + 000067 scan_results + 000095 content_blob_id +
// 000096 outbox trace_context),
// so the test doesn't run the full document migration chain. The harness
// then applies the real 000107 file on top. No RLS: the container
// superuser exercises the flow; tenant scoping is pinned by the
// prod-posture suite.
const completeFixtureDDL = `
CREATE TABLE organizations (
	id         UUID PRIMARY KEY,
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
CREATE TABLE content_blobs (
	id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id         UUID NOT NULL REFERENCES organizations(id),
	sha256_hash       TEXT NOT NULL,
	storage_region    TEXT NOT NULL DEFAULT 'us-east-1',
	storage_bucket    TEXT NOT NULL,
	storage_key       TEXT NOT NULL,
	storage_class     TEXT NOT NULL DEFAULT 'hot'
	                       CHECK (storage_class IN ('hot', 'warm', 'cold', 'quarantine')),
	size_bytes        BIGINT NOT NULL,
	mime_type         TEXT,
	encryption_key_id TEXT,
	encrypted_dek     BYTEA,
	dek_nonce         BYTEA,
	kek_id            TEXT,
	reference_count   INT NOT NULL DEFAULT 1,
	created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (tenant_id, sha256_hash)
);
CREATE TABLE scan_results (
	tenant_id  UUID NOT NULL REFERENCES organizations(id),
	id         UUID NOT NULL DEFAULT gen_random_uuid(),
	upload_id  UUID NOT NULL,
	result     TEXT NOT NULL,
	signature  TEXT,
	scanned_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (tenant_id, id)
);
CREATE TABLE tenant_upload_policies (
	tenant_id          UUID         PRIMARY KEY,
	allowed_mime_types JSONB        NOT NULL DEFAULT '[]'::jsonb,
	allowed_extensions JSONB        NOT NULL DEFAULT '[]'::jsonb,
	updated_at         TIMESTAMPTZ  NOT NULL DEFAULT now(),
	updated_by         UUID
);
CREATE TABLE tenant_keks (
	tenant_id   UUID        NOT NULL REFERENCES organizations(id),
	version     INT         NOT NULL CHECK (version > 0),
	alias       TEXT        NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	retired_at  TIMESTAMPTZ,
	retired_by  UUID,
	PRIMARY KEY (tenant_id, version)
);
CREATE TABLE outbox (
	id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	tenant_id       UUID NOT NULL,
	event_type      TEXT NOT NULL,
	aggregate_type  TEXT NOT NULL,
	aggregate_id    UUID NOT NULL,
	payload         JSONB NOT NULL,
	published       BOOLEAN NOT NULL DEFAULT false,
	created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at    TIMESTAMPTZ,
	actor_id        UUID,
	actor_name      TEXT,
	ip_address      INET,
	user_agent      TEXT,
	trace_context   JSONB
);
`

// migration000107 is the real document migration that persists the
// upload-session object location (H-9 / DEC-9).
const migration000107 = "000107_upload_session_object_key.up.sql"

type completeHarness struct {
	ctx    context.Context
	svc    *Service
	s3     *pkgstorage.S3Client
	pool   *pgxpool.Pool
	kms    pkgcrypto.KeyManager
	tenant uuid.UUID
}

type harnessConfig struct {
	// encrypt wires a LocalKeyManager; EncryptAtRest then defaults true.
	encrypt bool
	// beforeMigration runs against the pre-000107 schema (legacy rows).
	beforeMigration func(h *completeHarness)
}

func newCompleteHarness(t *testing.T) *completeHarness {
	return newHarness(t, harnessConfig{encrypt: true})
}

func newHarness(t *testing.T, cfg harnessConfig) *completeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgClean)
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, completeFixtureDDL)
	require.NoError(t, err)

	endpoint, access, secret, minioClean, err := testutil.NewMinIOContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(minioClean)
	s3c, err := pkgstorage.NewS3Client(endpoint, access, secret, false)
	require.NoError(t, err)
	require.NoError(t, s3c.CreateBucket(ctx, bucketName("us-east-1", "hot"), "us-east-1"))

	tenant := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	h := &completeHarness{ctx: ctx, s3: s3c, pool: pool, tenant: tenant}
	if cfg.encrypt {
		kek := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
		lkm, err := pkgcrypto.NewLocalKeyManager(kek, func(string) {})
		require.NoError(t, err)
		h.kms = lkm
	}
	if cfg.beforeMigration != nil {
		cfg.beforeMigration(h)
	}
	up, err := os.ReadFile(filepath.Join("..", "..", "..", "document", "migrations", migration000107))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(up))
	require.NoError(t, err)

	h.svc = h.newService(repository.New(pool))
	return h
}

// newService builds a Service over the harness's DB/S3/KMS with the given
// repo bundle (tests decorate repos for fault injection).
func (h *completeHarness) newService(repos *repository.Bundle) *Service {
	return New(Config{
		Pool:          h.pool,
		Repos:         repos,
		S3:            h.s3,
		Outbox:        database.NewOutboxRepository(),
		KMS:           h.kms,
		Logger:        zerolog.Nop(),
		DefaultRegion: "us-east-1",
	})
}

// objectKeyAt is the content-addressable key layout spelled out literally
// ({tenant}/{yyyy}/{mm}/{upload_id}/{filename}) so tests pin the on-disk
// shape independently of the production helper.
func objectKeyAt(tenant, uploadID uuid.UUID, filename string, at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("%s/%04d/%02d/%s/%s", tenant, at.Year(), int(at.Month()), uploadID, filename)
}

// previousMonth returns a mid-month instant in the month before now, so a
// key derived from it never shares the current month's prefix.
func previousMonth() time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month()-1, 15, 12, 0, 0, 0, time.UTC)
}

// httpGet fetches a presigned URL and returns status + body.
func httpGet(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec // test-only presigned URL
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, body
}
