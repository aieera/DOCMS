package repository

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DocumentFacts is what a document READ permission decision needs to know
// about a document (I-2): where it lives — workspace and folder, for the
// policy's inheritance rules — and its lifecycle state (the disposed
// deny). The same keys the document service and the GraphQL gateway send
// with a view check. Read from the document service's schema of record.
type DocumentFacts struct {
	ID             uuid.UUID
	WorkspaceID    uuid.UUID
	FolderID       uuid.UUID
	LifecycleState string
}

// LoadDocumentFacts returns the live document's facts, or nil (no error)
// when the document does not exist or is in the trash. Run it inside
// database.WithTenantTx.
func LoadDocumentFacts(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) (*DocumentFacts, error) {
	f := DocumentFacts{ID: docID}
	err := tx.QueryRow(ctx, `
		SELECT workspace_id, folder_id, lifecycle_state
		  FROM documents
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, docID).Scan(&f.WorkspaceID, &f.FolderID, &f.LifecycleState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}
