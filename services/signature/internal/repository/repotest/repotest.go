//go:build integration

// Package repotest is the signature service's integration-test harness.
//
// The signature service has no migrations of its own: every table it touches
// (signature_requests, signature_signers, tsp_signing_sessions,
// qes_certificates, esign_*) is owned by the document migration chain. New
// starts a prod-posture Postgres (testutil.NewProdPostureDB: dms_app
// NOBYPASSRLS, posture gate armed), runs that chain as the superuser, and
// re-grants dms_app on the resulting tables, so every signature integration
// test runs under the production RLS posture rather than the dev superuser.
package repotest

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// DocumentMigrationsDir is the absolute path of the document migration chain,
// resolved from this file so callers in any package can use it.
func DocumentMigrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	// .../services/signature/internal/repository/repotest/repotest.go
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "document", "migrations")
}

// New returns a migrated prod-posture database. Super bypasses RLS and is
// for seeding only; App is the dms_app pool the code under test must use.
func New(t testing.TB) *testutil.ProdPostureDB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	db := testutil.NewProdPostureDB(ctx, t, "")

	// document_entities is owned by the intelligence chain but ALTERed by
	// document's 000021+; pre-create it so the document chain completes on
	// a clean database (same workaround as the task service's fixtures).
	if _, err := db.Super.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS document_entities (
			tenant_id    UUID NOT NULL,
			id           UUID NOT NULL DEFAULT gen_random_uuid(),
			version_id   UUID NOT NULL,
			document_id  UUID NOT NULL,
			entity_type  TEXT NOT NULL,
			entity_value TEXT NOT NULL,
			start_offset INT  NOT NULL DEFAULT 0,
			end_offset   INT  NOT NULL DEFAULT 0,
			confidence   REAL NOT NULL DEFAULT 0,
			is_pii       BOOLEAN NOT NULL DEFAULT false,
			detected_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id)
		)`); err != nil {
		t.Fatalf("pre-create document_entities: %v", err)
	}
	if err := database.RunMigrations(db.SuperDSN, DocumentMigrationsDir()); err != nil {
		t.Fatalf("document migrations: %v", err)
	}
	// NewProdPostureDB granted dms_app on the tables that existed before the
	// chain ran (none); grant again now that the schema exists.
	if _, err := db.Super.Exec(ctx, `
		ALTER ROLE dms_app NOBYPASSRLS;
		GRANT USAGE ON SCHEMA public TO dms_app;
		GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO dms_app;
		GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO dms_app;
	`); err != nil {
		t.Fatalf("grant dms_app: %v", err)
	}
	// Fresh connections: none may carry state from before the grants.
	db.App.Reset()
	return db
}

// Tenant is one seeded organization with an owner and a plain member.
type Tenant struct {
	ID          uuid.UUID
	Owner       uuid.UUID
	OwnerEmail  string
	Member      uuid.UUID
	MemberEmail string
}

// SeedTenant inserts an organization plus an owner and a member user.
func SeedTenant(ctx context.Context, t testing.TB, super *pgxpool.Pool) Tenant {
	t.Helper()
	tn := Tenant{
		ID:     uuid.Must(uuid.NewV7()),
		Owner:  uuid.Must(uuid.NewV7()),
		Member: uuid.Must(uuid.NewV7()),
	}
	tn.OwnerEmail = "owner-" + tn.Owner.String() + "@test.local"
	tn.MemberEmail = "member-" + tn.Member.String() + "@test.local"
	if _, err := super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, 'Sig Test Org', $2)`,
		tn.ID, "sig-"+tn.ID.String()); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	for _, u := range []struct {
		id    uuid.UUID
		email string
		role  string
	}{{tn.Owner, tn.OwnerEmail, "owner"}, {tn.Member, tn.MemberEmail, "member"}} {
		if _, err := super.Exec(ctx, `
			INSERT INTO users (tenant_id, id, email, display_name, role)
			VALUES ($1, $2, $3, 'Sig Test User', $4)`,
			tn.ID, u.id, u.email, u.role); err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	return tn
}

// SeedUser adds one more user to an existing tenant.
func SeedUser(ctx context.Context, t testing.TB, super *pgxpool.Pool, tenantID uuid.UUID, role string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	email := role + "-" + id.String() + "@test.local"
	if _, err := super.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role)
		VALUES ($1, $2, $3, 'Sig Test User', $4)`,
		tenantID, id, email, role); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id, email
}

// SeedDocument inserts workspace → folder → document → blob → version and
// returns the document and version ids (the FK targets of
// signature_requests).
func SeedDocument(ctx context.Context, t testing.TB, super *pgxpool.Pool, tenantID, createdBy uuid.UUID) (docID, versionID uuid.UUID) {
	t.Helper()
	wsID := uuid.Must(uuid.NewV7())
	folderID := uuid.Must(uuid.NewV7())
	docID = uuid.Must(uuid.NewV7())
	versionID = uuid.Must(uuid.NewV7())
	blobID := uuid.Must(uuid.NewV7())
	sha := "sha-" + blobID.String()
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspaces (tenant_id, id, name) VALUES ($1, $2, 'ws')`, []any{tenantID, wsID}},
		{`INSERT INTO folders (tenant_id, id, workspace_id, name, path, depth)
		  VALUES ($1, $2, $3, 'root', 'root', 0)`, []any{tenantID, folderID, wsID}},
		{`INSERT INTO documents (tenant_id, id, workspace_id, folder_id, title, region_pin, created_by)
		  VALUES ($1, $2, $3, $4, 'Contract', 'us-east-1', $5)`, []any{tenantID, docID, wsID, folderID, createdBy}},
		{`INSERT INTO content_blobs (id, tenant_id, sha256_hash, storage_bucket, storage_key, size_bytes)
		  VALUES ($1, $2, $3, 'b', $4, 10)`, []any{blobID, tenantID, sha, "k/" + blobID.String()}},
		{`INSERT INTO document_versions (tenant_id, id, document_id, version_number, content_blob_id, size_bytes, sha256_hash, created_by)
		  VALUES ($1, $2, $3, 1, $4, 10, $5, $6)`, []any{tenantID, versionID, docID, blobID, sha, createdBy}},
	}
	for _, s := range stmts {
		if _, err := super.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("seed document chain: %v\n%s", err, s.sql)
		}
	}
	return docID, versionID
}
