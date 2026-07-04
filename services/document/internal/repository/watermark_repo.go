package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// WatermarkRepository backs the dynamic viewer watermark config (§5): per-tenant
// defaults + per-classification overrides. All methods take a tx opened inside
// database.WithTenantTx so RLS applies.
type WatermarkRepository interface {
	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (model.WatermarkConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, cfg model.WatermarkConfig) error

	ListOverrides(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.WatermarkOverride, error)
	UpsertOverride(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, o model.WatermarkOverride) (model.WatermarkOverride, error)
	DeleteOverride(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
}

type watermarkRepo struct{}

// NewWatermarkRepo constructs the stateless repo.
func NewWatermarkRepo() WatermarkRepository { return &watermarkRepo{} }

func (r *watermarkRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (model.WatermarkConfig, error) {
	cfg := model.DefaultWatermarkConfig()
	err := tx.QueryRow(ctx, `
		SELECT enabled, template, opacity, rotation_deg, tile, font_size, color
		  FROM watermark_config WHERE tenant_id = $1`, tenantID).
		Scan(&cfg.Enabled, &cfg.Template, &cfg.Opacity, &cfg.RotationDeg, &cfg.Tile, &cfg.FontSize, &cfg.Color)
	if err == pgx.ErrNoRows {
		return cfg, nil // absent row = default (disabled)
	}
	if err != nil {
		return model.WatermarkConfig{}, mapPgError(err)
	}
	return cfg, nil
}

func (r *watermarkRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, cfg model.WatermarkConfig) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO watermark_config
		    (tenant_id, enabled, template, opacity, rotation_deg, tile, font_size, color, updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
		    enabled = EXCLUDED.enabled,
		    template = EXCLUDED.template,
		    opacity = EXCLUDED.opacity,
		    rotation_deg = EXCLUDED.rotation_deg,
		    tile = EXCLUDED.tile,
		    font_size = EXCLUDED.font_size,
		    color = EXCLUDED.color,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = now()`,
		tenantID, cfg.Enabled, cfg.Template, cfg.Opacity, cfg.RotationDeg, cfg.Tile, cfg.FontSize, cfg.Color, actor)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *watermarkRepo) ListOverrides(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.WatermarkOverride, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, classification, enabled, opacity, tile, force, description, created_at,
		       COALESCE(created_by, '00000000-0000-0000-0000-000000000000'::uuid)
		  FROM watermark_classification_overrides WHERE tenant_id = $1
		 ORDER BY classification`, tenantID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []model.WatermarkOverride{}
	for rows.Next() {
		var o model.WatermarkOverride
		if err := rows.Scan(&o.ID, &o.Classification, &o.Enabled, &o.Opacity, &o.Tile,
			&o.Force, &o.Description, &o.CreatedAt, &o.CreatedBy); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// UpsertOverride inserts or (on the unique (tenant, classification)) updates the
// override — re-adding a classification edits it rather than erroring.
func (r *watermarkRepo) UpsertOverride(ctx context.Context, tx pgx.Tx, tenantID, actor uuid.UUID, in model.WatermarkOverride) (model.WatermarkOverride, error) {
	var out model.WatermarkOverride
	err := tx.QueryRow(ctx, `
		INSERT INTO watermark_classification_overrides
		    (tenant_id, classification, enabled, opacity, tile, force, description, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (tenant_id, classification) DO UPDATE SET
		    enabled = EXCLUDED.enabled,
		    opacity = EXCLUDED.opacity,
		    tile = EXCLUDED.tile,
		    force = EXCLUDED.force,
		    description = EXCLUDED.description
		RETURNING id, classification, enabled, opacity, tile, force, description, created_at,
		          COALESCE(created_by, '00000000-0000-0000-0000-000000000000'::uuid)`,
		tenantID, in.Classification, in.Enabled, in.Opacity, in.Tile, in.Force, in.Description, actor).
		Scan(&out.ID, &out.Classification, &out.Enabled, &out.Opacity, &out.Tile,
			&out.Force, &out.Description, &out.CreatedAt, &out.CreatedBy)
	if err != nil {
		return model.WatermarkOverride{}, mapPgError(err)
	}
	return out, nil
}

func (r *watermarkRepo) DeleteOverride(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	ct, err := tx.Exec(ctx, `DELETE FROM watermark_classification_overrides WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}
