package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// ClassificationRepository backs classification-based access control (§8): the
// per-tenant gate config, the classification→access rules, per-user clearance,
// and the denormalisation of the compliance scan onto the documents row. All
// methods take a tx opened inside database.WithTenantTx so RLS applies.
type ClassificationRepository interface {
	GetGateConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (model.ClassificationGateConfig, error)
	UpsertGateConfig(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, cfg model.ClassificationGateConfig) error

	ListRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.ClassificationRule, error)
	CreateRule(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, r model.ClassificationRule) (model.ClassificationRule, error)
	DeleteRule(ctx context.Context, tx pgx.Tx, tenantID, ruleID uuid.UUID) (bool, error)

	GetUserClearance(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (string, error)
	SetUserClearance(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, clearance string) (bool, error)

	// SetDocumentSensitivity denormalises the compliance scan onto the row.
	// It always updates the PHI/PII flags, but only overwrites
	// security_classification when the current source is not an authoritative
	// manual/records marking (a human label is never lowered by a scan).
	SetDocumentSensitivity(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, class string, hasPHI, hasPII bool) error
	// SetDocumentClassificationManual is the admin override path.
	SetDocumentClassificationManual(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, class string) (bool, error)
}

type classificationRepo struct{}

// NewClassificationRepo constructs the stateless repo.
func NewClassificationRepo() ClassificationRepository { return &classificationRepo{} }

func (r *classificationRepo) GetGateConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (model.ClassificationGateConfig, error) {
	cfg := model.ClassificationGateConfig{Enabled: false, PHIRequiresRestricted: true}
	err := tx.QueryRow(ctx, `
		SELECT enabled, phi_requires_restricted
		  FROM classification_gate_config WHERE tenant_id = $1`, tenantID).
		Scan(&cfg.Enabled, &cfg.PHIRequiresRestricted)
	if err == pgx.ErrNoRows {
		return cfg, nil // absent row = disabled
	}
	if err != nil {
		return model.ClassificationGateConfig{}, mapPgError(err)
	}
	return cfg, nil
}

func (r *classificationRepo) UpsertGateConfig(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, cfg model.ClassificationGateConfig) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO classification_gate_config (tenant_id, enabled, phi_requires_restricted, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled = EXCLUDED.enabled,
		       phi_requires_restricted = EXCLUDED.phi_requires_restricted,
		       updated_by = EXCLUDED.updated_by,
		       updated_at = now()`,
		tenantID, cfg.Enabled, cfg.PHIRequiresRestricted, actor)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *classificationRepo) ListRules(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.ClassificationRule, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, min_classification, action, required_clearance, applies_to_phi,
		       description, created_at, COALESCE(created_by, '00000000-0000-0000-0000-000000000000'::uuid)
		  FROM classification_access_rules WHERE tenant_id = $1
		 ORDER BY created_at`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []model.ClassificationRule{}
	for rows.Next() {
		var m model.ClassificationRule
		if err := rows.Scan(&m.ID, &m.MinClassification, &m.Action, &m.RequiredClearance,
			&m.AppliesToPHI, &m.Description, &m.CreatedAt, &m.CreatedBy); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *classificationRepo) CreateRule(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, in model.ClassificationRule) (model.ClassificationRule, error) {
	var out model.ClassificationRule
	err := tx.QueryRow(ctx, `
		INSERT INTO classification_access_rules
		    (tenant_id, min_classification, action, required_clearance, applies_to_phi, description, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, min_classification, action, required_clearance, applies_to_phi, description, created_at,
		          COALESCE(created_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		tenantID, in.MinClassification, in.Action, in.RequiredClearance, in.AppliesToPHI, in.Description, actor).
		Scan(&out.ID, &out.MinClassification, &out.Action, &out.RequiredClearance, &out.AppliesToPHI,
			&out.Description, &out.CreatedAt, &out.CreatedBy)
	if err != nil {
		return model.ClassificationRule{}, mapPgError(err)
	}
	return out, nil
}

func (r *classificationRepo) DeleteRule(ctx context.Context, tx pgx.Tx, tenantID, ruleID uuid.UUID) (bool, error) {
	ct, err := tx.Exec(ctx, `DELETE FROM classification_access_rules WHERE tenant_id = $1 AND id = $2`, tenantID, ruleID)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}

func (r *classificationRepo) GetUserClearance(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) (string, error) {
	var clearance string
	err := tx.QueryRow(ctx, `SELECT COALESCE(clearance, '') FROM users WHERE tenant_id = $1 AND id = $2`, tenantID, userID).Scan(&clearance)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", mapPgError(err)
	}
	return clearance, nil
}

func (r *classificationRepo) SetUserClearance(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, clearance string) (bool, error) {
	ct, err := tx.Exec(ctx, `UPDATE users SET clearance = $3 WHERE tenant_id = $1 AND id = $2`, tenantID, userID, clearance)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}

func (r *classificationRepo) SetDocumentSensitivity(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, class string, hasPHI, hasPII bool) error {
	// PHI/PII flags always reflect the latest scan. security_classification is
	// only (re)derived from the scan when a human hasn't set it — a 'manual' or
	// 'records' source is authoritative and never lowered here.
	_, err := tx.Exec(ctx, `
		UPDATE documents
		   SET has_phi = $3,
		       has_pii = $4,
		       security_classification = CASE
		           WHEN classification_source IN ('manual','records') THEN security_classification
		           ELSE $5 END,
		       classification_source = CASE
		           WHEN classification_source IN ('manual','records') THEN classification_source
		           ELSE 'scan' END
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, docID, hasPHI, hasPII, class)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *classificationRepo) SetDocumentClassificationManual(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, class string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE documents
		   SET security_classification = $3, classification_source = 'manual'
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, docID, class)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}
