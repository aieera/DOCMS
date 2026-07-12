//go:build integration
// +build integration

// Blueprint §4.7 (outbox) + §5.1 (event topology) + ADR 0021.
// These are the two acceptance tests deferred from Wave 5 Prompt 5.1
// (remediation 12a, DoD row 3 + rollback). They pin:
//
//  1. CreateVersion → `dms.version.uploaded.v1` lands on NATS within 2 s
//     with the full 12-field VersionUploadedV1 payload.
//  2. If the outbox insert fails, the version row is rolled back (no
//     partial commit, no lost event).
//
// Run with: go test -tags integration ./services/document/internal/service/...
package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// ---------------------------------------------------------------------------
// Test 1 — end-to-end publish
// ---------------------------------------------------------------------------

func TestCreateVersion_PublishesVersionUploadedEventViaOutbox(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)
	natsURL, natsCleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(natsCleanup)

	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	// Testcontainer superuser has BYPASSRLS — skip the posture gate like
	// the sibling integration tests do (latent here until ADR 0121
	// unblocked the migration chain and this code became reachable).
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	nc, js, err := events.ConnectNATS(natsURL)
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	seedOrg(ctx, t, pool, tenant)
	seedUser(ctx, t, pool, tenant, user)

	repos := repository.New(pool)
	svc := service.New(pool, repos, stubPolicy{allow: true}, zerolog.Nop())

	callCtx := auth.SetTenantID(ctx, tenant)
	callCtx = auth.WithUser(callCtx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	// Subscribe BEFORE the publisher starts so we don't race on the first
	// outbox drain tick (publisher polls every 50 ms in this test).
	received := make(chan *nats.Msg, 4)
	sub, err := nc.ChanSubscribe("dms.version.uploaded.v1", received)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	// Seed workspace/folder/document by raw SQL — the existing CreateFolder
	// / CreateDocument paths depend on a parent_id column that is still
	// called parent_folder_id in the migration (unrelated drift, out of
	// scope for §5.1). We exercise only CreateVersion here, which is the
	// SUT for the event emission.
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())
	seedWorkspaceFolderDoc(ctx, t, pool, tenant, wsID, folderID, docID)

	blobID := uuid.Must(uuid.NewV7())
	const bucket = "vaultdms-us-east-1"
	blobKey := fmt.Sprintf("tenants/%s/blobs/%s", tenant, blobID)
	seedContentBlob(ctx, t, pool, tenant, blobID, bucket, blobKey, 4242, "application/pdf")

	// Start the publisher now so the only inbound publish is the one we
	// trigger below.
	pub := database.NewOutboxPublisherWithConfig(
		pool, js, "document", zerolog.Nop(),
		database.PublisherConfig{PollInterval: 50 * time.Millisecond},
	)
	pubCtx, cancelPub := context.WithCancel(ctx)
	t.Cleanup(cancelPub)
	go pub.Start(pubCtx)
	t.Cleanup(pub.Stop)

	v, err := svc.CreateVersion(callCtx, &service.CreateVersionInput{
		DocumentID:    docID,
		ContentBlobID: blobID,
		SizeBytes:     4242,
		MimeType:      "application/pdf",
		SHA256Hash:    "deadbeefcafebabe",
	})
	require.NoError(t, err)

	// Wait up to 2 s for the event (requirement from the task spec).
	var msg *nats.Msg
	select {
	case msg = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("no dms.version.uploaded.v1 message within 2 s")
	}

	// CloudEvents envelope shape — written by the outbox publisher.
	var env struct {
		SpecVersion     string          `json:"specversion"`
		Type            string          `json:"type"`
		Source          string          `json:"source"`
		DataContentType string          `json:"datacontenttype"`
		TenantID        string          `json:"tenantid"`
		Data            json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(msg.Data, &env))
	require.Equal(t, "1.0", env.SpecVersion)
	require.Equal(t, "dms.version.uploaded.v1", env.Type)
	require.Equal(t, "vaultdms.document", env.Source)
	require.Equal(t, "application/json", env.DataContentType)
	require.Equal(t, tenant.String(), env.TenantID)

	// Payload shape per ADR 0021 / model.VersionUploadedPayload. We assert
	// every field that the intelligence pipeline depends on.
	var p model.VersionUploadedPayload
	require.NoError(t, json.Unmarshal(env.Data, &p))
	require.NotEmpty(t, p.EventID, "event_id")
	require.Equal(t, tenant.String(), p.TenantID)
	require.Equal(t, docID.String(), p.DocumentID)
	require.Equal(t, v.ID.String(), p.VersionID)
	require.Equal(t, v.VersionNumber, p.VersionNumber)
	require.Equal(t, blobID.String(), p.ContentBlobID)
	require.Equal(t, "s3://"+bucket+"/"+blobKey, p.StorageURI)
	require.Equal(t, "application/pdf", p.MimeType)
	require.Equal(t, int64(4242), p.SizeBytes)
	require.Equal(t, "deadbeefcafebabe", p.SHA256)
	require.Equal(t, user.String(), p.UploadedByUserID)
	_, terr := time.Parse(time.RFC3339, p.UploadedAt)
	require.NoError(t, terr, "uploaded_at must be RFC3339")

	// Nats-Msg-Id is the dedupe key (outbox event id, not version id).
	require.NotEmpty(t, msg.Header.Get("Nats-Msg-Id"))
}

// ---------------------------------------------------------------------------
// Test 2 — outbox failure rolls back the version write
// ---------------------------------------------------------------------------

// failingOutbox returns errOutboxInjected on every Insert. It is substituted
// for the real outbox repository to prove that CreateVersion honors the
// transactional-outbox invariant (§4.7): no version row may be committed
// unless the outbox insert also commits.
type failingOutbox struct{}

var errOutboxInjected = errors.New("injected outbox failure")

func (failingOutbox) Insert(context.Context, pgx.Tx, *model.OutboxEvent) error {
	return errOutboxInjected
}

func TestCreateVersion_RollsBackWhenOutboxInsertFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	dsn, pgCleanup, err := testutil.NewPostgresContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(pgCleanup)

	require.NoError(t, database.RunMigrations(dsn, "../../migrations"))

	// Testcontainer superuser has BYPASSRLS — skip the posture gate like
	// the sibling integration tests do (latent here until ADR 0121
	// unblocked the migration chain and this code became reachable).
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	tenant := uuid.Must(uuid.NewV7())
	user := uuid.Must(uuid.NewV7())
	seedOrg(ctx, t, pool, tenant)
	seedUser(ctx, t, pool, tenant, user)

	callCtx := auth.SetTenantID(ctx, tenant)
	callCtx = auth.WithUser(callCtx, auth.UserInfo{ID: user, TenantID: tenant, Role: "admin"})

	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID := uuid.Must(uuid.NewV7())
	seedWorkspaceFolderDoc(ctx, t, pool, tenant, wsID, folderID, docID)

	blobID := uuid.Must(uuid.NewV7())
	seedContentBlob(ctx, t, pool, tenant, blobID,
		"vaultdms-us-east-1", "k/"+blobID.String(), 100, "application/pdf")

	// Build a repository bundle with a failing Outbox. CreateVersion must
	// fail AND leave no version row behind (§4.7: atomic outbox).
	repos := repository.New(pool)
	repos.Outbox = failingOutbox{}
	svc := service.New(pool, repos, stubPolicy{allow: true}, zerolog.Nop())

	_, err = svc.CreateVersion(callCtx, &service.CreateVersionInput{
		DocumentID:    docID,
		ContentBlobID: blobID,
		SizeBytes:     100,
		MimeType:      "application/pdf",
		SHA256Hash:    "a1b2c3",
	})
	require.Error(t, err, "expected outbox failure to surface")
	require.ErrorIs(t, err, errOutboxInjected)

	// No version row for this document — the tx rolled back.
	var n int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2",
		tenant, docID).Scan(&n))
	require.Equal(t, 0, n, "version must not be persisted when outbox insert fails")

	// And documents.current_version_id must still be NULL.
	var cv *uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT current_version_id FROM documents WHERE tenant_id = $1 AND id = $2",
		tenant, docID).Scan(&cv))
	require.Nil(t, cv, "documents.current_version_id must not be advanced on rollback")
}

// ---------------------------------------------------------------------------
// Seed helpers
// ---------------------------------------------------------------------------

func seedOrg(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)
	`, tenant, "t", "t-"+tenant.String()[:8])
	require.NoError(t, err)
}

// seedWorkspaceFolderDoc inserts the parent rows CreateVersion requires:
// one workspace, one root folder (parent_folder_id NULL, depth 0), and one
// draft document under that folder. All inserts are done as the container's
// superuser so we bypass RLS in setup — the write path under test still
// runs through WithTenantTx and the RLS-enforced dms policies.
func seedUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, tenant, id uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Test User', 'admin')
	`, tenant, id, fmt.Sprintf("u-%s@test.local", id.String()[:8]))
	require.NoError(t, err)
}

func seedWorkspaceFolderDoc(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	tenant, workspaceID, folderID, documentID uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')
	`, tenant, workspaceID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		VALUES ($1, $2, $3, 'root', 'root', 0)
	`, tenant, folderID, workspaceID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, description,
		                       region_pin, sha256_hash, mime_type)
		VALUES ($1, $2, $3, $4, 'doc', '', 'us-east-1', '', '')
	`, tenant, documentID, workspaceID, folderID)
	require.NoError(t, err)
}

func seedContentBlob(ctx context.Context, t *testing.T, pool *pgxpool.Pool,
	tenant, id uuid.UUID, bucket, key string, size int64, mime string) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO content_blobs
		  (id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
		   storage_class, size_bytes, mime_type)
		VALUES ($1, $2, $3, 'us-east-1', $4, $5, 'hot', $6, $7)
	`, id, tenant, "sha-"+id.String()[:8], bucket, key, size, mime)
	require.NoError(t, err)
}
