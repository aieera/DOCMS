package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

// ClaimBlobTx exposes the DEC-12 blob-claim check (claimBlobTx) to sibling
// write paths that run their own tenant transaction — bulk import links
// caller-supplied blob ids. Same contract: the blob is returned only if
// userID completed the upload session that produced it or the caller in
// ctx can view a live document already carrying it; anything else
// (including a blob that does not exist) is vdmserr.ErrNotFound.
func (s *DocumentService) ClaimBlobTx(ctx context.Context, tx pgx.Tx, tenantID, userID, blobID uuid.UUID) (*repository.ContentBlob, error) {
	return s.claimBlobTx(ctx, tx, tenantID, userID, blobID)
}

// AssertNotHeldTx exposes the DEC-7 legal-hold gate (assertNotHeld: the
// in-band legal_hold state and the legal_hold_documents binding) to
// sibling write paths. Callers row-lock the document first
// (repository.LockDocument) so a hold committed concurrently is seen.
func (s *DocumentService) AssertNotHeldTx(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID, op string) error {
	return s.assertNotHeld(ctx, tx, tenantID, docID, op)
}
