package autolink

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

// pgStore is the Postgres-backed Store. Every method opens its own
// tenant transaction (SET LOCAL app.current_tenant) so RLS applies —
// the consumer has no inbound request context to inherit one from.
type pgStore struct {
	pool *pgxpool.Pool
}

// NewStore wraps the pool in the Store interface used by the consumer.
func NewStore(pool *pgxpool.Pool) Store {
	return &pgStore{pool: pool}
}

func (s *pgStore) DocumentMetadata(ctx context.Context, tenantID, docID uuid.UUID) (map[string]any, error) {
	var raw []byte
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT custom_metadata FROM documents
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		`, tenantID, docID).Scan(&raw)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			// Deleted between event and processing — nothing to link.
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("autolink metadata read: %w", err)
	}
	meta := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &meta); err != nil {
			return nil, fmt.Errorf("autolink metadata decode: %w", err)
		}
	}
	return meta, nil
}

func (s *pgStore) FindTargets(ctx context.Context, tenantID uuid.UUID, r Rule, self uuid.UUID) ([]uuid.UUID, error) {
	// All shapes match through `custom_metadata @> {key: value}` so the
	// GIN index (idx_documents_metadata_gin) carries the lookup.
	containment, err := json.Marshal(map[string]string{r.Key: r.Value})
	if err != nil {
		return nil, err
	}
	q := `
		SELECT id FROM documents
		 WHERE tenant_id = $1
		   AND custom_metadata @> $2::jsonb
		   AND id <> $3
		   AND deleted_at IS NULL`
	args := []any{tenantID, string(containment), self}

	switch r.Kind {
	case "pointer":
		// Target must BE the entity the pointer names.
		q += ` AND custom_metadata->>'erp_entity_type' = $4`
		args = append(args, r.EntityType)
	case "reverse-pointer":
		// Targets are OTHER entity types carrying our identity key.
		q += ` AND (custom_metadata->>'erp_entity_type') IS DISTINCT FROM $4`
		args = append(args, r.EntityType)
	}
	// Fetch one past the cap so the consumer can log the truncation.
	q += fmt.Sprintf(" LIMIT %d", maxTargetsPerRule+1)

	var out []uuid.UUID
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("autolink match (%s %s): %w", r.Kind, r.Key, err)
	}
	return out, nil
}

func (s *pgStore) InsertEdge(ctx context.Context, tenantID, src, dst uuid.UUID, confidence float64, meta map[string]any) error {
	if src == dst {
		return nil // schema CHECK would reject; skip cheaply
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// created_by stays NULL — extractor-created edge (ADR 0099).
		// The unique (tenant, src, dst, type) index makes redelivery and
		// re-runs idempotent.
		_, err := tx.Exec(ctx, `
			INSERT INTO contract_graph_edges
			    (tenant_id, src_document, dst_document, edge_type, confidence, metadata)
			VALUES ($1, $2, $3, 'references', $4, $5::jsonb)
			ON CONFLICT (tenant_id, src_document, dst_document, edge_type) DO NOTHING
		`, tenantID, src, dst, confidence, string(metaJSON))
		return err
	})
	if err != nil {
		return fmt.Errorf("autolink edge insert: %w", err)
	}
	return nil
}
