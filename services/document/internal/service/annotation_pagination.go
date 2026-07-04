package service

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// maxAnnotationPageSize caps a single page so a caller can't pull an
// unbounded result set; ListAnnotations (no limit) is still available for
// the overlay, which legitimately needs every annotation on the version.
const maxAnnotationPageSize = 500

// AnnotationPage is one page of annotations plus the opaque cursor for the
// next page ("" when the result set is exhausted).
type AnnotationPage struct {
	Items      []model.Annotation
	NextCursor string
}

// ListAnnotationsPage is the paginated form of ListAnnotations, opt-in via
// the REST ?limit/?cursor params. Same "view" gate as ListAnnotations.
// Keyset-paginated by (page_number, created_at, id).
func (s *DocumentService) ListAnnotationsPage(ctx context.Context, documentID, versionID uuid.UUID, limit int, cursorStr string) (AnnotationPage, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return AnnotationPage{}, err
	}
	if limit <= 0 || limit > maxAnnotationPageSize {
		limit = maxAnnotationPageSize
	}
	cursor, err := decodeAnnotationCursor(cursorStr)
	if err != nil {
		return AnnotationPage{}, vdmserr.Validation("cursor", "invalid pagination cursor")
	}

	var page AnnotationPage
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if s.repos.Annotations == nil {
			return vdmserr.Internal("annotations repository not wired")
		}
		// Fetch one extra row to detect whether a further page exists.
		rows, err := s.repos.Annotations.ListByDocumentVersionPage(ctx, tx, tenantID, documentID, versionID, limit+1, cursor)
		if err != nil {
			return err
		}
		if len(rows) > limit {
			last := rows[limit-1]
			page.NextCursor = encodeAnnotationCursor(repository.AnnotationCursor{
				Page: last.PageNumber, CreatedAt: last.CreatedAt, ID: last.ID,
			})
			rows = rows[:limit]
		}
		page.Items = rows
		return nil
	})
	return page, err
}

// encodeAnnotationCursor / decodeAnnotationCursor round-trip the keyset
// position as an opaque, URL-safe token: base64("page|createdAtUnixNano|id").
func encodeAnnotationCursor(c repository.AnnotationCursor) string {
	raw := fmt.Sprintf("%d|%d|%s", c.Page, c.CreatedAt.UTC().UnixNano(), c.ID.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAnnotationCursor(s string) (*repository.AnnotationCursor, error) {
	if s == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(string(b), "|", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed cursor")
	}
	page, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, err
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return nil, err
	}
	id, err := uuid.Parse(parts[2])
	if err != nil {
		return nil, err
	}
	return &repository.AnnotationCursor{Page: page, CreatedAt: time.Unix(0, nanos).UTC(), ID: id}, nil
}
