package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DocumentFacts is what a permission decision needs to know about a
// document: where it lives (folder / workspace inheritance), its lifecycle
// state and its sensitivity (classification gate). Read from the document
// service's schema-of-record (documents table, document 000001 + 000087).
type DocumentFacts struct {
	WorkspaceID            uuid.UUID
	FolderID               uuid.UUID
	LifecycleState         string
	SecurityClassification string
	HasPHI                 bool
	HasPII                 bool
}

// ClassificationRule mirrors one classification_access_rules row.
type ClassificationRule struct {
	MinClassification string
	Action            string
	RequiredClearance string
	AppliesToPHI      bool
}

// ClassificationGate is the tenant's classification gating config (absent
// row = disabled) and its rules.
type ClassificationGate struct {
	Enabled               bool
	PHIRequiresRestricted bool
	Rules                 []ClassificationRule
}

// ResourceContextRepo reads the facts the HTTP permission surface resolves
// server-side instead of trusting the caller (M-11, CB-3).
type ResourceContextRepo interface {
	// DocumentFacts returns nil (no error) for a missing or trashed document.
	DocumentFacts(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) (*DocumentFacts, error)
	// FolderWorkspace returns ok=false for a missing or trashed folder.
	FolderWorkspace(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) (workspaceID uuid.UUID, ok bool, err error)
	// UserFacts returns the user's status and clearance ("" when unknown).
	UserFacts(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (status, clearance string, err error)
	ClassificationGate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (ClassificationGate, error)
}

type resourceContextRepo struct{}

func (resourceContextRepo) DocumentFacts(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) (*DocumentFacts, error) {
	var f DocumentFacts
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, folder_id, lifecycle_state,
		       security_classification, has_phi, has_pii
		FROM documents
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, docID).
		Scan(&f.WorkspaceID, &f.FolderID, &f.LifecycleState, &f.SecurityClassification, &f.HasPHI, &f.HasPII)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	return &f, nil
}

func (resourceContextRepo) FolderWorkspace(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) (uuid.UUID, bool, error) {
	var ws uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT workspace_id FROM folders
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tenantID, folderID).Scan(&ws)
	if err == pgx.ErrNoRows {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, mapPgError(err)
	}
	return ws, true, nil
}

func (resourceContextRepo) UserFacts(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (string, string, error) {
	var status, clearance string
	err := tx.QueryRow(ctx, `
		SELECT status, COALESCE(clearance, '') FROM users
		WHERE tenant_id = $1 AND id = $2`, tenantID, userID).Scan(&status, &clearance)
	if err == pgx.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", mapPgError(err)
	}
	return status, clearance, nil
}

func (resourceContextRepo) ClassificationGate(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (ClassificationGate, error) {
	g := ClassificationGate{PHIRequiresRestricted: true}
	err := tx.QueryRow(ctx, `
		SELECT enabled, phi_requires_restricted
		FROM classification_gate_config WHERE tenant_id = $1`, tenantID).
		Scan(&g.Enabled, &g.PHIRequiresRestricted)
	if err == pgx.ErrNoRows {
		return ClassificationGate{}, nil // absent row = disabled
	}
	if err != nil {
		return ClassificationGate{}, mapPgError(err)
	}
	if !g.Enabled {
		return g, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT min_classification, action, required_clearance, applies_to_phi
		FROM classification_access_rules WHERE tenant_id = $1
		ORDER BY created_at`, tenantID)
	if err != nil {
		return ClassificationGate{}, mapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r ClassificationRule
		if err := rows.Scan(&r.MinClassification, &r.Action, &r.RequiredClearance, &r.AppliesToPHI); err != nil {
			return ClassificationGate{}, mapPgError(err)
		}
		g.Rules = append(g.Rules, r)
	}
	return g, mapPgError(rows.Err())
}
