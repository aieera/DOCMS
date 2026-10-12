package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// FilterViewableDocumentIDs returns the subset of ids the caller may view
// (live documents only), resolved with one BatchCheckPermission carrying the
// same per-row OPA context and classification gate as list reads. Fails
// closed: a policy-transport error is returned, never an unfiltered set. Used
// by read surfaces that hydrate documents found through a side table (the
// contract graph, D-19) rather than through the document list path.
func (s *DocumentService) FilterViewableDocumentIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]bool{}
	if userID == uuid.Nil || len(ids) == 0 {
		return out, nil
	}
	docs := make([]model.Document, 0, len(ids))
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		for _, id := range ids {
			d, gerr := s.repos.Documents.GetByID(ctx, tx, tenantID, id)
			if errors.Is(gerr, vdmserr.ErrNotFound) {
				continue
			}
			if gerr != nil {
				return gerr
			}
			if d.DeletedAt == nil {
				docs = append(docs, *d)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return out, nil
	}
	viewable, err := s.filterViewableDocuments(ctx, userID, docs)
	if err != nil {
		return nil, err
	}
	for i := range viewable {
		out[viewable[i].ID] = true
	}
	return out, nil
}
