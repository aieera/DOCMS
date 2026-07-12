//go:build integration
// +build integration

// CompleteUpload idempotency + concurrency (the retry-corruption fix).
//
// The old flow treated a re-complete of an already-completed session as
// an "idempotent dupe" and FELL THROUGH into the full pipeline: the size
// check then compared the client's declared PLAINTEXT size against the
// object envelope encryption had already overwritten with CIPHERTEXT,
// failUpload flipped the completed session to failed, and the
// sha-mismatch branch could even DELETE the stored object. Client
// retries after a timeout are normal, so this fired in the wild.
//
// Pinned here against real Postgres + real MinIO + real envelope
// encryption (LocalKeyManager), scanner disabled:
//
//   - double-complete returns the identical success and mutates nothing
//     (status stays completed, one blob, refcount 1, object intact,
//     one scan record);
//   - N concurrent completes yield exactly ONE pipeline execution —
//     losers get the replayed success or a retryable 409, never a
//     second blob / refcount bump / scan;
//   - a genuinely wrong-size upload still fails its FIRST complete;
//   - a wrong declared sha still fails its FIRST complete.
package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	pkgcrypto "github.com/aieera/sedoc/pkg/crypto"
	"github.com/aieera/sedoc/pkg/database"
	pkgstorage "github.com/aieera/sedoc/pkg/storage"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/storage/internal/model"
	"github.com/aieera/sedoc/services/storage/internal/repository"
)

// completeFixtureDDL — verbatim shapes of the tables CompleteUpload
// touches (document 000001 + 000003 encrypted_dek + 000067 scan_results
// + 000095 content_blob_id), so the test doesn't run the full document
// migration chain. No RLS: the container superuser exercises the flow;
// tenant scoping is pinned by the prod-posture suite.
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
	storage_class     TEXT NOT NULL DEFAULT 'hot',
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
	user_agent      TEXT
);
`

type completeHarness struct {
	ctx    context.Context
	svc    *Service
	s3     *pkgstorage.S3Client
	tenant uuid.UUID
}

func newCompleteHarness(t *testing.T) *completeHarness {
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

	kek := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	lkm, err := pkgcrypto.NewLocalKeyManager(kek, func(string) {})
	require.NoError(t, err)

	tenant := uuid.Must(uuid.NewV7())
	_, err = pool.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, tenant)
	require.NoError(t, err)

	svc := New(Config{
		Pool:          pool,
		Repos:         repository.New(pool),
		S3:            s3c,
		Outbox:        database.NewOutboxRepository(),
		KMS:           lkm, // EncryptAtRest defaults true → the ciphertext-overwrite hazard is live
		Logger:        zerolog.Nop(),
		DefaultRegion: "us-east-1",
	})

	return &completeHarness{ctx: ctx, svc: svc, s3: s3c, tenant: tenant}
}

// seedUpload creates an initiated session and PUTs the payload at its
// content-addressable key — the state right after a client's presigned
// PUT, i.e. the input to CompleteUpload.
func seedUpload(t *testing.T, h *completeHarness, svc *Service, payload []byte, declaredSize int64) (uuid.UUID, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	session := &model.UploadSession{
		TenantID:      h.tenant,
		ID:            id,
		Filename:      "retry-me.pdf",
		TotalSize:     declaredSize,
		MimeType:      "application/pdf",
		UploadType:    model.UploadType("single"),
		StorageRegion: "us-east-1",
		Status:        model.UploadInitiated,
		CreatedAt:     time.Now().UTC(),
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
	}
	require.NoError(t, database.WithTenantTx(h.ctx, svc.pool, h.tenant, func(tx pgx.Tx) error {
		return svc.repos.Uploads.Create(h.ctx, tx, session)
	}))
	key := contentAddressableKey(h.tenant, id, session.Filename)
	require.NoError(t, h.s3.PutObject(h.ctx, bucketName("us-east-1", "hot"), key,
		bytes.NewReader(payload), int64(len(payload)), "application/pdf"))
	sum := sha256.Sum256(payload)
	return id, hex.EncodeToString(sum[:])
}

func (h *completeHarness) counts(t *testing.T, svc *Service, uploadID uuid.UUID) (blobs, scans int, status string, refcount int) {
	t.Helper()
	pool := svc.pool
	require.NoError(t, pool.QueryRow(h.ctx,
		`SELECT count(*) FROM content_blobs WHERE tenant_id=$1`, h.tenant).Scan(&blobs))
	require.NoError(t, pool.QueryRow(h.ctx,
		`SELECT count(*) FROM scan_results WHERE tenant_id=$1 AND upload_id=$2`, h.tenant, uploadID).Scan(&scans))
	require.NoError(t, pool.QueryRow(h.ctx,
		`SELECT status FROM upload_sessions WHERE tenant_id=$1 AND id=$2`, h.tenant, uploadID).Scan(&status))
	_ = pool.QueryRow(h.ctx,
		`SELECT reference_count FROM content_blobs WHERE tenant_id=$1 LIMIT 1`, h.tenant).Scan(&refcount)
	return
}

func TestCompleteUpload_RetryAfterSuccessIsIdempotent(t *testing.T) {
	h := newCompleteHarness(t)
	payload := []byte("the full correct plaintext bytes of a client upload")
	uploadID, sha := seedUpload(t, h, h.svc, payload, int64(len(payload)))

	in := CompleteUploadInput{TenantID: h.tenant, UploadID: uploadID, SHA256Hash: sha, SizeBytes: int64(len(payload))}
	first, err := h.svc.CompleteUpload(h.ctx, in)
	require.NoError(t, err, "first complete must succeed")

	// The retry a client sends after a timeout. Before the fix this
	// re-ran the pipeline against the (now encrypted) object: the size
	// check failed, the completed session was flipped to failed, and
	// the sha branch could delete the stored object.
	second, err := h.svc.CompleteUpload(h.ctx, in)
	require.NoError(t, err, "retry-after-success must replay the success, not corrupt it")
	require.Equal(t, first.StorageBucket, second.StorageBucket)
	require.Equal(t, first.StorageKey, second.StorageKey)
	require.Equal(t, first.SizeBytes, second.SizeBytes, "replay reports the PLAINTEXT size, not ciphertext")
	require.Equal(t, first.SHA256Hash, second.SHA256Hash)
	require.Equal(t, first.Tier, second.Tier)
	require.Equal(t, first.ScanResult, second.ScanResult, "replay reports the ORIGINAL scan outcome")

	blobs, scans, status, refcount := h.counts(t, h.svc, uploadID)
	require.Equal(t, "completed", status, "retry must not mutate session state")
	require.Equal(t, 1, blobs, "retry must not insert a second blob")
	require.Equal(t, 1, refcount, "retry must not bump the refcount")
	require.Equal(t, 1, scans, "retry must not re-scan")

	// The stored object survived (the old sha-mismatch branch deleted it).
	_, err = h.s3.GetObjectInfo(h.ctx, second.StorageBucket, second.StorageKey)
	require.NoError(t, err, "stored object must still exist after a retried complete")
}

func TestCompleteUpload_ConcurrentCompletesYieldOneCompletion(t *testing.T) {
	h := newCompleteHarness(t)
	payload := []byte("concurrent completes race for this exact payload")
	uploadID, sha := seedUpload(t, h, h.svc, payload, int64(len(payload)))
	in := CompleteUploadInput{TenantID: h.tenant, UploadID: uploadID, SHA256Hash: sha, SizeBytes: int64(len(payload))}

	const n = 8
	var wg sync.WaitGroup
	results := make([]*CompleteUploadResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = h.svc.CompleteUpload(h.ctx, in)
		}(i)
	}
	wg.Wait()

	successes := 0
	for i := 0; i < n; i++ {
		switch {
		case errs[i] == nil:
			successes++
			require.Equal(t, sha, results[i].SHA256Hash, "every success must be THE completion's result")
		case strings.Contains(errs[i].Error(), "already in progress"):
			// loser of the claim — retryable by contract
		default:
			t.Fatalf("unexpected error shape from concurrent complete: %v", errs[i])
		}
	}
	require.GreaterOrEqual(t, successes, 1, "at least the claim winner succeeds")

	blobs, scans, status, refcount := h.counts(t, h.svc, uploadID)
	require.Equal(t, "completed", status)
	require.Equal(t, 1, blobs, "exactly one completion pipeline may run")
	require.Equal(t, 1, refcount, "no dedup-refcount bump from a racing duplicate")
	require.Equal(t, 1, scans, "exactly one scan")

	// And the retryable losers converge on the same success afterwards.
	replay, err := h.svc.CompleteUpload(h.ctx, in)
	require.NoError(t, err)
	require.Equal(t, sha, replay.SHA256Hash)
}

func TestCompleteUpload_WrongSizeStillFailsFirstComplete(t *testing.T) {
	h := newCompleteHarness(t)
	payload := []byte("these bytes are shorter than the client declared")
	uploadID, sha := seedUpload(t, h, h.svc, payload, int64(len(payload))+1000) // declared ≠ actual

	_, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{
		TenantID: h.tenant, UploadID: uploadID, SHA256Hash: sha, SizeBytes: int64(len(payload)) + 1000,
	})
	require.Error(t, err, "legitimate size validation must still fail bad uploads")
	require.Contains(t, err.Error(), "size mismatch")

	var status string
	require.NoError(t, h.svc.pool.QueryRow(h.ctx,
		`SELECT status FROM upload_sessions WHERE tenant_id=$1 AND id=$2`, h.tenant, uploadID).Scan(&status))
	require.Equal(t, "failed", status)
}

func TestCompleteUpload_WrongShaStillFailsFirstComplete(t *testing.T) {
	h := newCompleteHarness(t)
	payload := []byte("bytes whose hash will not match the declaration")
	uploadID, _ := seedUpload(t, h, h.svc, payload, int64(len(payload)))

	wrong := sha256.Sum256([]byte("some other content entirely"))
	_, err := h.svc.CompleteUpload(h.ctx, CompleteUploadInput{
		TenantID: h.tenant, UploadID: uploadID,
		SHA256Hash: hex.EncodeToString(wrong[:]), SizeBytes: int64(len(payload)),
	})
	require.Error(t, err, "sha validation must still fail bad uploads")
	require.Contains(t, err.Error(), "hash")
}
