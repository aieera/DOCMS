// Package bulk implements the gRPC + HTTP bulk import/export surface
// described in ADR 0075. Lives in the document service because
// workspaces, folders, and documents are document-owned; users and
// groups are dispatched outbound to the auth service via gRPC.
package bulk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// ResourceType is one of the five bulk-supported resource kinds.
// String values match the CHECK constraint on bulk_external_id_map.
type ResourceType string

const (
	ResourceWorkspace ResourceType = "workspace"
	ResourceFolder    ResourceType = "folder"
	ResourceDocument  ResourceType = "document"
	ResourceUser      ResourceType = "user"
	ResourceGroup     ResourceType = "group"
)

// Repo persists the external_id <-> internal UUID map and the
// per-request idempotency log.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// LookupExternal returns the internal UUID for a (tenant, type,
// external_id) triple, or uuid.Nil + nil if no mapping exists yet.
// Runs inside the supplied tx so the surrounding processor can
// coalesce reads + writes.
func (r *Repo) LookupExternal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind ResourceType, externalID string) (uuid.UUID, error) {
	if externalID == "" {
		return uuid.Nil, nil
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT internal_id FROM bulk_external_id_map
		WHERE tenant_id = $1 AND resource_type = $2 AND external_id = $3
	`, tenantID, string(kind), externalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, nil
	}
	return id, err
}

// RecordExternal inserts a new (external_id, internal_id) mapping.
// Idempotent via ON CONFLICT — re-inserting the same external_id
// keeps the existing internal id (we never re-mint).
func (r *Repo) RecordExternal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind ResourceType, externalID string, internalID uuid.UUID) error {
	if externalID == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO bulk_external_id_map (tenant_id, resource_type, external_id, internal_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, resource_type, external_id) DO NOTHING
	`, tenantID, string(kind), externalID, internalID)
	return err
}

// ImportLogStatus is the persisted state of a BulkImportRequest.
type ImportLogStatus struct {
	RequestID    uuid.UUID
	ItemsDigest  string
	Status       string
	ItemCount    int
	SuccessCount int
	FailureCount int
	ResponseJSON []byte
}

// LookupRequest checks if a (tenant, request_id) pair has been
// processed. Returns the stored response so retries can replay
// the same per-item Result without re-running processors.
func (r *Repo) LookupRequest(ctx context.Context, tenantID, requestID uuid.UUID) (*ImportLogStatus, error) {
	var out ImportLogStatus
	err := database.WithTenantTx(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT request_id, items_digest, status, item_count, success_count, failure_count, response_json
			FROM bulk_import_log
			WHERE tenant_id = $1 AND request_id = $2
		`, tenantID, requestID).Scan(
			&out.RequestID, &out.ItemsDigest, &out.Status,
			&out.ItemCount, &out.SuccessCount, &out.FailureCount, &out.ResponseJSON,
		)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// SaveRequest writes the final per-request status. Called once per
// BulkImportRequest after every item has been processed (or
// errored). Use ON CONFLICT to make repeated saves a no-op so
// crash-during-save doesn't deadlock retries.
func (r *Repo) SaveRequest(ctx context.Context, tenantID uuid.UUID, st *ImportLogStatus) error {
	tenantUUID := tenantID
	return database.WithTenantTx(ctx, r.pool, tenantUUID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO bulk_import_log (
				tenant_id, request_id, items_digest, status,
				item_count, success_count, failure_count,
				response_json, started_at, completed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now())
			ON CONFLICT (tenant_id, request_id) DO UPDATE SET
				status = EXCLUDED.status,
				success_count = EXCLUDED.success_count,
				failure_count = EXCLUDED.failure_count,
				response_json = EXCLUDED.response_json,
				completed_at = now()
		`, tenantID, st.RequestID, st.ItemsDigest, st.Status,
			st.ItemCount, st.SuccessCount, st.FailureCount, st.ResponseJSON)
		return err
	})
}

// DigestItems produces a stable hex SHA-256 of a JSON-canonicalized
// item list. Used to detect a request_id collision where the items
// don't actually match — likely a client bug, surfaced as a 409.
func DigestItems(payload any) string {
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FmtDigestMismatch is the error returned when a replay carries the
// same request_id but different items than the original call.
func FmtDigestMismatch(requestID uuid.UUID) error {
	return fmt.Errorf("request_id %s previously seen with different items; refusing to replay", requestID)
}
