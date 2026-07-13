// Search reconcile — rebuild a document's OpenSearch projection from
// source of truth.
//
// Why this exists: the search index is a projection fed by events, and a
// projection can rot — most recently the indexer's full-replace-on-
// sparse-update bug wiped readable_by + content from every doc whose
// metadata was edited (the doc then matched no user's ACL filter and
// vanished from search). Fixing the indexer stops NEW wipes; this
// reconcile path repairs docs that were already wiped, and doubles as
// the recovery tool for any future projection drift.
//
// Mechanics: for each live document we rebuild the FULL index payload
// from Postgres — the documents row (title/desc/tags/lifecycle/...),
// the current version (mime/size/count), the OCR text (ocr_results,
// which the intelligence worker persists per page), and the folder ACL
// (computeFolderReaders — the same query that stamps readable_by on
// created events). The payload is emitted as dms.document.reindexed.v1
// through the transactional outbox (never direct publish); the search
// indexer full-replaces the index doc from it.
//
// Trigger: POST /internal/v1/search/reindex (whole tenant, or one doc
// via {"document_id": "..."}). See docs/runbooks/search-reindex.md.
package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// reindexBatchSize bounds documents per transaction so a whole-tenant
// reindex neither holds a long tx nor builds an unbounded outbox write.
const reindexBatchSize = 200

// reindexContentCap bounds the OCR text shipped per document (bytes of
// text, roughly). OpenSearch analyses the field anyway; a runaway scan
// result must not produce a multi-MB outbox row.
const reindexContentCap = 1_000_000

// ReindexSearch rebuilds the search projection for one document (id
// non-nil) or every live document in the tenant (id nil). Returns the
// number of reindex events emitted. Caller must be tenant-scoped;
// intended for the internal reindex endpoint, not end users.
func (s *DocumentService) ReindexSearch(ctx context.Context, documentID *uuid.UUID) (int, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return 0, err
	}

	total := 0
	var cursor uuid.UUID // keyset: docs with id > cursor, batch-ordered
	for {
		n := 0
		err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			var innerErr error
			n, cursor, innerErr = s.reindexBatch(ctx, tx, tenantID, documentID, cursor)
			return innerErr
		})
		if err != nil {
			return total, err
		}
		total += n
		if documentID != nil || n < reindexBatchSize {
			return total, nil
		}
	}
}

// reindexBatch emits reindex events for up to reindexBatchSize docs
// after `cursor` (or exactly the one doc when documentID is set) inside
// the caller's tx. Returns (emitted, lastID).
func (s *DocumentService) reindexBatch(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, documentID *uuid.UUID, cursor uuid.UUID) (int, uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT d.id, d.workspace_id, d.folder_id, d.title, COALESCE(d.description,''),
		       d.tags, COALESCE(d.document_class,''), d.lifecycle_state, d.region_pin,
		       COALESCE(d.mime_type,''), d.total_size_bytes, d.created_by, d.current_version_id
		FROM documents d
		WHERE d.tenant_id = $1 AND d.deleted_at IS NULL
		  AND ($2::uuid IS NULL OR d.id = $2)
		  AND ($2::uuid IS NOT NULL OR d.id > $3)
		ORDER BY d.id
		LIMIT $4
	`, tenantID, documentID, cursor, reindexBatchSize)
	if err != nil {
		return 0, cursor, fmt.Errorf("list documents: %w", err)
	}
	type docRow struct {
		id, workspaceID     uuid.UUID
		folderID            *uuid.UUID
		title, description  string
		tags                []string
		documentClass       string
		lifecycleState      string
		regionPin, mimeType string
		sizeBytes           int64
		createdBy           *uuid.UUID
		currentVersionID    *uuid.UUID
	}
	var batch []docRow
	for rows.Next() {
		var d docRow
		if err := rows.Scan(&d.id, &d.workspaceID, &d.folderID, &d.title, &d.description,
			&d.tags, &d.documentClass, &d.lifecycleState, &d.regionPin,
			&d.mimeType, &d.sizeBytes, &d.createdBy, &d.currentVersionID); err != nil {
			rows.Close()
			return 0, cursor, err
		}
		batch = append(batch, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, cursor, err
	}
	if documentID != nil && len(batch) == 0 {
		return 0, cursor, vdmserr.ErrNotFound
	}

	emitted := 0
	last := cursor
	for _, d := range batch {
		last = d.id

		// OCR text of the current version — the same content the
		// ocr_completed event shipped, re-read from its Postgres home.
		var content string
		var versionCount int
		if d.currentVersionID != nil {
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(LEFT(string_agg(text_content, E'\n' ORDER BY page_number), $3), '')
				FROM ocr_results WHERE tenant_id = $1 AND version_id = $2
			`, tenantID, d.currentVersionID, reindexContentCap).Scan(&content); err != nil {
				return emitted, last, fmt.Errorf("ocr text for %s: %w", d.id, err)
			}
		}
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM document_versions WHERE tenant_id = $1 AND document_id = $2
		`, tenantID, d.id).Scan(&versionCount); err != nil {
			return emitted, last, fmt.Errorf("version count for %s: %w", d.id, err)
		}

		// ACL projection — identical computation to the one stamped on
		// created events / permission.changed (FIX-4).
		var readableBy, users, groups []string
		if d.folderID != nil {
			readableBy, users, groups, err = s.computeFolderReaders(ctx, tx, tenantID, *d.folderID, d.workspaceID)
			if err != nil {
				return emitted, last, fmt.Errorf("compute readers for %s: %w", d.id, err)
			}
		}

		payload := map[string]any{
			"document_id":        d.id.String(),
			"workspace_id":       d.workspaceID.String(),
			"title":              d.title,
			"description":        d.description,
			"tags":               d.tags,
			"document_class":     d.documentClass,
			"lifecycle_state":    d.lifecycleState,
			"region_pin":         d.regionPin,
			"mime_type":          d.mimeType,
			"size_bytes":         d.sizeBytes,
			"version_count":      versionCount,
			"content":            content,
			"content_snippet":    snippet(content, 500),
			"readable_by":        readableBy,
			"readable_by_users":  users,
			"readable_by_groups": groups,
		}
		if d.folderID != nil {
			payload["folder_id"] = d.folderID.String()
		}
		if d.createdBy != nil {
			payload["created_by"] = d.createdBy.String()
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.document.reindexed.v1", "document", d.id, payload)
		if err != nil {
			return emitted, last, err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return emitted, last, err
		}
		emitted++
	}
	return emitted, last, nil
}

func snippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
