// NER repo (ADR 0078) — reads from document_entities (written by the
// intelligence service's ner.py) and writes to entity_corrections.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Entity is one row of document_entities — populated by regex / SpaCy
// / LLM in the intelligence service and surfaced by the document
// service's GET /documents/{id}/entities endpoint.
type Entity struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	DocumentID  uuid.UUID
	VersionID   uuid.UUID
	EntityType  string
	EntityValue string
	StartOffset int32
	EndOffset   int32
	Confidence  float32
	IsPII       bool
	Source      string // 'spacy'|'llm'|'regex'|'manual'
	DetectedAt  time.Time
}

type EntityCorrection struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	DocumentID       uuid.UUID
	VersionID        uuid.UUID
	OriginalEntityID *uuid.UUID
	OriginalType     string
	CorrectedType    string
	EntityValue      string
	StartOffset      int32
	EndOffset        int32
	Action           string // 'relabel'|'add'|'delete'|'confirm'
	Note             string
	CorrectedBy      uuid.UUID
	CreatedAt        time.Time
}

type EntityCorrectionInput struct {
	DocumentID       uuid.UUID
	VersionID        uuid.UUID
	OriginalEntityID *uuid.UUID
	OriginalType     string
	CorrectedType    string
	EntityValue      string
	StartOffset      int32
	EndOffset        int32
	Action           string
	Note             string
}

type ListEntitiesOpts struct {
	VersionID  *uuid.UUID // nil = current version (resolved by repo)
	EntityType string     // optional filter
	Source     string     // optional filter
	OnlyPII    bool       // optional filter
	Limit      int32
	Offset     int32
}

// NERConfig — per-tenant LLM toggle, mirrored from the ner_config
// table. Defaults match migration 000021. Encrypted key fields added
// in migration 000022 are populated by the API-key Set/Clear methods
// only — never written by UpsertConfig.
type NERConfig struct {
	Enabled              bool
	Model                string
	EntityTypes          []string
	BatchSize            int32
	MinConfidence        float32
	APIKeyEncrypted      string     // base64(nonce || ciphertext); empty = unset
	APIKeySetAt          *time.Time // nil when unset
}

type NERConfigPatch struct {
	Enabled       *bool
	Model         *string
	EntityTypes   *[]string
	BatchSize     *int32
	MinConfidence *float32
}

type NERRepository interface {
	ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, opts ListEntitiesOpts) ([]Entity, int64, error)
	GetEntity(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID) (*Entity, error)
	UpdateEntityType(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID, newType string) error
	DeleteEntity(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID) error
	InsertEntity(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, e Entity) (*Entity, error)
	InsertCorrection(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, in EntityCorrectionInput) (*EntityCorrection, error)
	ListCorrections(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]EntityCorrection, error)
	CurrentVersionID(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (uuid.UUID, error)
	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*NERConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p NERConfigPatch) (*NERConfig, error)
	SetEncryptedAPIKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, encrypted string, setAt time.Time) error
	ClearAPIKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error
}

type nerRepo struct{}

func NewNERRepo() NERRepository { return &nerRepo{} }

const selectEntityCols = `
    id, tenant_id, document_id, version_id, entity_type, entity_value,
    start_offset, end_offset, confidence, is_pii, source, detected_at`

func scanEntity(row pgx.Row) (*Entity, error) {
	var e Entity
	if err := row.Scan(
		&e.ID, &e.TenantID, &e.DocumentID, &e.VersionID,
		&e.EntityType, &e.EntityValue,
		&e.StartOffset, &e.EndOffset,
		&e.Confidence, &e.IsPII, &e.Source, &e.DetectedAt,
	); err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *nerRepo) CurrentVersionID(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (uuid.UUID, error) {
	var versionID uuid.UUID
	err := tx.QueryRow(ctx, `
        SELECT id FROM document_versions
         WHERE tenant_id = $1 AND document_id = $2
         ORDER BY version_number DESC
         LIMIT 1`,
		tenantID, documentID,
	).Scan(&versionID)
	return versionID, err
}

func (r *nerRepo) ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID, opts ListEntitiesOpts) ([]Entity, int64, error) {
	versionID := uuid.Nil
	if opts.VersionID != nil {
		versionID = *opts.VersionID
	} else {
		v, err := r.CurrentVersionID(ctx, tx, tenantID, documentID)
		if err != nil {
			return nil, 0, err
		}
		versionID = v
	}

	limit := opts.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}

	// Build WHERE filters; the leading (tenant_id, version_id) is
	// covered by idx_document_entities_version, so we can layer
	// optional predicates on top without losing the index.
	args := []any{tenantID, versionID}
	wh := "tenant_id = $1 AND version_id = $2"
	if opts.EntityType != "" {
		args = append(args, opts.EntityType)
		wh += " AND entity_type = $" + itoa(len(args))
	}
	if opts.Source != "" {
		args = append(args, opts.Source)
		wh += " AND source = $" + itoa(len(args))
	}
	if opts.OnlyPII {
		wh += " AND is_pii = true"
	}

	var total int64
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM document_entities WHERE "+wh,
		args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, limit, opts.Offset)
	rows, err := tx.Query(ctx,
		"SELECT "+selectEntityCols+" FROM document_entities WHERE "+wh+
			" ORDER BY start_offset ASC LIMIT $"+itoa(len(args)-1)+" OFFSET $"+itoa(len(args)),
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]Entity, 0, 64)
	for rows.Next() {
		e, sErr := scanEntity(rows)
		if sErr != nil {
			return nil, 0, sErr
		}
		out = append(out, *e)
	}
	return out, total, rows.Err()
}

func (r *nerRepo) GetEntity(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID) (*Entity, error) {
	row := tx.QueryRow(ctx,
		"SELECT "+selectEntityCols+" FROM document_entities WHERE tenant_id=$1 AND id=$2",
		tenantID, entityID,
	)
	return scanEntity(row)
}

func (r *nerRepo) UpdateEntityType(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID, newType string) error {
	_, err := tx.Exec(ctx,
		`UPDATE document_entities
		    SET entity_type = $3, source = 'manual'
		  WHERE tenant_id = $1 AND id = $2`,
		tenantID, entityID, newType)
	return err
}

func (r *nerRepo) DeleteEntity(ctx context.Context, tx pgx.Tx, tenantID, entityID uuid.UUID) error {
	_, err := tx.Exec(ctx,
		"DELETE FROM document_entities WHERE tenant_id=$1 AND id=$2",
		tenantID, entityID)
	return err
}

func (r *nerRepo) InsertEntity(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, e Entity) (*Entity, error) {
	row := tx.QueryRow(ctx, `
        INSERT INTO document_entities (
            tenant_id, version_id, document_id,
            entity_type, entity_value, start_offset, end_offset,
            confidence, is_pii, source, detected_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7, $8, $9, 'manual', now()
        )
        RETURNING `+selectEntityCols,
		tenantID, e.VersionID, e.DocumentID,
		e.EntityType, e.EntityValue, e.StartOffset, e.EndOffset,
		e.Confidence, e.IsPII,
	)
	return scanEntity(row)
}

func (r *nerRepo) InsertCorrection(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, in EntityCorrectionInput) (*EntityCorrection, error) {
	row := tx.QueryRow(ctx, `
        INSERT INTO entity_corrections (
            tenant_id, document_id, version_id,
            original_entity_id, original_type, corrected_type,
            entity_value, start_offset, end_offset,
            correction_action, note, corrected_by
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
        )
        RETURNING id, created_at`,
		tenantID, in.DocumentID, in.VersionID,
		in.OriginalEntityID, in.OriginalType, in.CorrectedType,
		in.EntityValue, in.StartOffset, in.EndOffset,
		in.Action, in.Note, userID,
	)
	out := &EntityCorrection{
		TenantID:         tenantID,
		DocumentID:       in.DocumentID,
		VersionID:        in.VersionID,
		OriginalEntityID: in.OriginalEntityID,
		OriginalType:     in.OriginalType,
		CorrectedType:    in.CorrectedType,
		EntityValue:      in.EntityValue,
		StartOffset:      in.StartOffset,
		EndOffset:        in.EndOffset,
		Action:           in.Action,
		Note:             in.Note,
		CorrectedBy:      userID,
	}
	if err := row.Scan(&out.ID, &out.CreatedAt); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *nerRepo) ListCorrections(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]EntityCorrection, error) {
	rows, err := tx.Query(ctx, `
        SELECT id, tenant_id, document_id, version_id,
               original_entity_id, COALESCE(original_type,''), corrected_type,
               entity_value, start_offset, end_offset,
               correction_action, COALESCE(note,''), corrected_by, created_at
          FROM entity_corrections
         WHERE tenant_id = $1 AND document_id = $2
         ORDER BY created_at DESC
         LIMIT 500`,
		tenantID, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EntityCorrection, 0, 32)
	for rows.Next() {
		var c EntityCorrection
		if err := rows.Scan(
			&c.ID, &c.TenantID, &c.DocumentID, &c.VersionID,
			&c.OriginalEntityID, &c.OriginalType, &c.CorrectedType,
			&c.EntityValue, &c.StartOffset, &c.EndOffset,
			&c.Action, &c.Note, &c.CorrectedBy, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *nerRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*NERConfig, error) {
	row := tx.QueryRow(ctx, `
        SELECT llm_enabled, llm_model, llm_entity_types,
               llm_batch_size, llm_min_confidence,
               COALESCE(llm_api_key_encrypted, ''), llm_api_key_set_at
          FROM ner_config WHERE tenant_id = $1`,
		tenantID)
	var c NERConfig
	if err := row.Scan(
		&c.Enabled, &c.Model, &c.EntityTypes, &c.BatchSize, &c.MinConfidence,
		&c.APIKeyEncrypted, &c.APIKeySetAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *nerRepo) SetEncryptedAPIKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, encrypted string, setAt time.Time) error {
	// Upsert: if no row exists yet (admin sets key before saving any
	// other config field), insert with migration defaults so the next
	// GetConfig works.
	_, err := tx.Exec(ctx, `
        INSERT INTO ner_config (tenant_id, llm_api_key_encrypted, llm_api_key_set_at, updated_at)
        VALUES ($1, $2, $3, now())
        ON CONFLICT (tenant_id) DO UPDATE SET
            llm_api_key_encrypted = EXCLUDED.llm_api_key_encrypted,
            llm_api_key_set_at    = EXCLUDED.llm_api_key_set_at,
            updated_at            = now()`,
		tenantID, encrypted, setAt)
	return err
}

func (r *nerRepo) ClearAPIKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
        UPDATE ner_config
           SET llm_api_key_encrypted = NULL,
               llm_api_key_set_at    = NULL,
               updated_at            = now()
         WHERE tenant_id = $1`,
		tenantID)
	return err
}

func (r *nerRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p NERConfigPatch) (*NERConfig, error) {
	// Defaults match migration 000021 — keep them in sync if the
	// migration ever changes.
	c := NERConfig{
		Enabled: false, Model: "claude-haiku-4-5",
		EntityTypes: []string{
			"party_name", "effective_date", "jurisdiction", "governing_law",
			"account_number", "tax_id", "patient_id", "address", "national_id",
		},
		BatchSize: 5, MinConfidence: 0.6,
	}
	if existing, _ := r.GetConfig(ctx, tx, tenantID); existing != nil {
		c = *existing
	}
	if p.Enabled != nil {
		c.Enabled = *p.Enabled
	}
	if p.Model != nil {
		c.Model = *p.Model
	}
	if p.EntityTypes != nil {
		c.EntityTypes = *p.EntityTypes
	}
	if p.BatchSize != nil {
		c.BatchSize = *p.BatchSize
	}
	if p.MinConfidence != nil {
		c.MinConfidence = *p.MinConfidence
	}
	_, err := tx.Exec(ctx, `
        INSERT INTO ner_config (
            tenant_id, llm_enabled, llm_model, llm_entity_types,
            llm_batch_size, llm_min_confidence, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, now())
        ON CONFLICT (tenant_id) DO UPDATE SET
            llm_enabled        = EXCLUDED.llm_enabled,
            llm_model          = EXCLUDED.llm_model,
            llm_entity_types   = EXCLUDED.llm_entity_types,
            llm_batch_size     = EXCLUDED.llm_batch_size,
            llm_min_confidence = EXCLUDED.llm_min_confidence,
            updated_at         = now()`,
		tenantID, c.Enabled, c.Model, c.EntityTypes, c.BatchSize, c.MinConfidence)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// itoa: small dependency-free integer→string for SQL placeholder building.
// strconv would work too — keeping the import set tight.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 4)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
