//go:build integration

// Regression for the compliance-finding review 500 (SQLSTATE 42804):
// UpdateFindingStatus set remediated_by via
//   CASE WHEN $4='open' THEN NULL ELSE $3 END
// pgx sends google/uuid.UUID through its driver.Valuer as TEXT, so the
// CASE resolved to text and text→uuid is not an implicit assignment cast
// for a uuid column — every Acknowledge / Mark-remediated / False-positive
// click 500ed. The fix casts the param ($3::uuid) inside the CASE.
//
// Runs against the dev DB (same harness as purge_cascade). Everything
// happens inside a rolled-back tx so no rows survive the test.
//
//	go test -tags integration ./services/document/internal/repository/...

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestUpdateFindingStatus_RemediatedByCastsToUUID(t *testing.T) {
	pool := mustOpenPool(t)
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tenantID := mustUUID(t, "864fef9c-386f-4c8c-aad0-7f23e49cf553") // seed acme tenant
	reviewer := mustUUID(t, "d1127315-d945-4caf-98ac-c700c3c14ab6") // admin@acme.local
	findingID := uuid.Must(uuid.NewV7())
	// Real seed-tenant document + version (compliance_findings FKs onto
	// documents/document_versions). The finding row itself is new and
	// rolled back, so this doesn't disturb the fixture doc.
	docID := mustUUID(t, "5872b090-10e9-4f99-bdba-eabab27b699b")
	versionID := mustUUID(t, "8d11dfe0-cdac-4493-8111-83a0a5e87b71")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // isolation: nothing is committed

	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String()); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	// Minimal finding row (only the NOT-NULL-without-default columns).
	if _, err := tx.Exec(ctx, `
		INSERT INTO compliance_findings
		    (id, tenant_id, document_id, version_id, entity_type, entity_category,
		     occurrence_count, confidence, risk_level, remediation_status)
		VALUES ($1,$2,$3,$4,'TEST_SSN','pii',1,0.9,'medium','open')`,
		findingID, tenantID, docID, versionID); err != nil {
		t.Fatalf("seed finding: %v", err)
	}

	repo := &complianceRepo{}

	// Non-open status: remediated_by must be set to the reviewer's uuid.
	// This is the exact path that 500ed with 42804 before the ::uuid cast.
	got, err := repo.UpdateFindingStatus(ctx, tx, tenantID, findingID, reviewer, "acknowledged", "looks fine")
	if err != nil {
		t.Fatalf("acknowledge failed (the 42804 bug if this is a type error): %v", err)
	}
	if got.RemediationStatus != "acknowledged" {
		t.Fatalf("status = %q, want acknowledged", got.RemediationStatus)
	}
	if got.RemediatedBy == nil || *got.RemediatedBy != reviewer {
		t.Fatalf("remediated_by = %v, want %v", got.RemediatedBy, reviewer)
	}

	// open reset: remediated_by must clear back to NULL (the CASE's NULL branch).
	got, err = repo.UpdateFindingStatus(ctx, tx, tenantID, findingID, reviewer, "open", "")
	if err != nil {
		t.Fatalf("open reset failed: %v", err)
	}
	if got.RemediatedBy != nil {
		t.Fatalf("remediated_by = %v after open reset, want nil", got.RemediatedBy)
	}

	_ = pgx.ErrNoRows // keep pgx import if future assertions need it
}
