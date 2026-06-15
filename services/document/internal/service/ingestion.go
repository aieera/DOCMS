package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// Pre-commit ingestion pipeline (Workstream 3). This file owns the service-
// layer surface for the staging-then-route flow:
//
//   - CreateIngestionItem stages a completed blob (POST /ingest) WITHOUT
//     minting a document/version, and emits dms.ingestion.received.v1 so the
//     intelligence worker OCRs + extracts a business key against the blob ref.
//   - The Route* methods are the in-process activities the IngestAndRoute
//     Temporal workflow drives after the worker emits dms.ingestion.processed.v1.
//     RouteCommit reuses the Workstream-1 UpsertDocumentByExternalKey path so a
//     high-confidence read becomes a new version of the matched document (or a
//     new document v1) with NO orphan staging document. RouteNeedsReview parks
//     a low-confidence / ambiguous read in the review queue — never auto-versioned.
//   - The review-queue read + resolve surface lets a human finish the routing.

// CreateIngestionInput is the request for POST /ingest.
type CreateIngestionInput struct {
	WorkspaceID       uuid.UUID
	FolderID          uuid.UUID // where a NEW document lands if no match
	TargetCustomerRef string
	BlobChecksum      string // sha256 hex; the bytes are already in S3
	BlobRef           string // optional content_blob UUID; verified vs checksum
	Mime              string
	Size              int64
	RegionPin         string
	DocumentClass     string // optional hint for the extraction profile
}

// CreateIngestionItem stages a completed blob for routing. Idempotent on
// (tenant, checksum, target): a re-POST returns the existing active staging row
// without emitting a second received event.
func (s *DocumentService) CreateIngestionItem(ctx context.Context, in *CreateIngestionInput) (*model.IngestionItem, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	// The committed document/version carries a created_by FK, so the ingest
	// must act as a real principal (the route step replays as this user).
	if userID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if in.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if in.FolderID == uuid.Nil {
		return nil, errInvalidInput("folder_id", "required")
	}
	if err := validateRegion(in.RegionPin); err != nil {
		return nil, err
	}
	checksum := strings.ToLower(strings.TrimSpace(in.BlobChecksum))
	if checksum == "" {
		return nil, errInvalidInput("blob_checksum", "required")
	}

	// Permission: staging routes into the workspace, so gate on folder edit —
	// the same gate the upsert create path enforces at commit time.
	if perr := s.requirePermission(ctx, userID, "edit", "folder", in.FolderID, map[string]any{
		"workspace_id": in.WorkspaceID.String(),
	}); perr != nil {
		return nil, perr
	}

	region := in.RegionPin
	if region == "" {
		region = "us-east-1"
	}

	var out *model.IngestionItem
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Resolve the content-addressed blob (validates it exists + dedup by
		// sha256) and read its S3 coordinates so the worker can fetch the bytes.
		blobID, _, sha, mime, rerr := s.resolveContentBlobTx(ctx, tx, tenantID, checksum, in.BlobRef)
		if rerr != nil {
			return rerr
		}
		bucket, key, serr := s.resolveBlobStorageTx(ctx, tx, tenantID, blobID)
		if serr != nil {
			return serr
		}
		if mime == "" {
			mime = in.Mime
		}
		folder := in.FolderID
		item := &model.IngestionItem{
			TenantID:          tenantID,
			WorkspaceID:       in.WorkspaceID,
			FolderID:          &folder,
			TargetCustomerRef: in.TargetCustomerRef,
			BlobRef:           blobID,
			BlobChecksum:      sha,
			Status:            model.IngestReceived,
			DocumentClass:     in.DocumentClass,
			StorageBucket:     bucket,
			StorageKey:        key,
			MimeType:          mime,
			RegionPin:         region,
			CreatedBy:         &userID,
		}
		created, existing, cerr := s.repos.Ingestion.Create(ctx, tx, item)
		if cerr != nil {
			return cerr
		}
		if !created {
			out = existing
			return nil
		}
		evt, eerr := model.NewOutboxEvent(tenantID, "dms.ingestion.received.v1", "ingestion", item.ID,
			model.IngestionReceivedPayload{
				IngestionItemID:   item.ID.String(),
				TenantID:          tenantID.String(),
				WorkspaceID:       item.WorkspaceID.String(),
				TargetCustomerRef: item.TargetCustomerRef,
				ContentBlobID:     item.BlobRef.String(),
				BlobChecksum:      item.BlobChecksum,
				StorageBucket:     item.StorageBucket,
				StorageKey:        item.StorageKey,
				MimeType:          item.MimeType,
				RegionPin:         item.RegionPin,
				DocumentClass:     item.DocumentClass,
			})
		if eerr != nil {
			return eerr
		}
		if oerr := s.repos.Outbox.Insert(ctx, tx, evt); oerr != nil {
			return oerr
		}
		out = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveBlobStorageTx reads the S3 bucket/key for a content blob so the
// intelligence worker can fetch + OCR the bytes off the received event.
func (s *DocumentService) resolveBlobStorageTx(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID) (bucket, key string, err error) {
	qerr := tx.QueryRow(ctx,
		`SELECT storage_bucket, storage_key FROM content_blobs
		  WHERE tenant_id = $1 AND id = $2 AND shredded_at IS NULL`,
		tenantID, blobID).Scan(&bucket, &key)
	if errors.Is(qerr, pgx.ErrNoRows) {
		return "", "", vdmserr.Validation("blob_ref", "content blob not found")
	}
	if qerr != nil {
		return "", "", qerr
	}
	return bucket, key, nil
}

// ---- Route activities (driven by the IngestAndRoute Temporal workflow) -----

// RouteLoad returns the staged item. Read-only; tenant context only (no user).
func (s *DocumentService) RouteLoad(ctx context.Context, tenantID, itemID uuid.UUID) (*model.IngestionItem, error) {
	var it *model.IngestionItem
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		x, e := s.repos.Ingestion.GetByID(ctx, tx, tenantID, itemID, false)
		it = x
		return e
	})
	return it, err
}

// RouteMatch looks up an existing document carrying the staged item's extracted
// external key. This is a SYSTEM lookup (not permission-filtered like
// GetDocumentByExternalKey): a match is a match regardless of who can view it.
// Returns nil when there's no key or no live document for it.
func (s *DocumentService) RouteMatch(ctx context.Context, tenantID, itemID uuid.UUID) (*uuid.UUID, error) {
	var match *uuid.UUID
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		it, e := s.repos.Ingestion.GetByID(ctx, tx, tenantID, itemID, false)
		if e != nil {
			return e
		}
		key := strings.TrimSpace(it.ExtractedExternalKey)
		if key == "" {
			return nil
		}
		doc, ge := s.repos.Documents.GetByExternalID(ctx, tx, tenantID, key, false)
		if ge != nil {
			if errors.Is(ge, vdmserr.ErrNotFound) {
				return nil
			}
			return ge
		}
		if doc.DeletedAt == nil {
			id := doc.ID
			match = &id
		}
		return nil
	})
	return match, err
}

// RouteCommitResult reports what RouteCommit did.
type RouteCommitResult struct {
	DocumentID  uuid.UUID
	VersionID   uuid.UUID
	NewDocument bool
}

// RouteCommit commits a high-confidence staged item via the Workstream-1
// upsert: a new version of the matched document, or a new document v1 when the
// key is unseen. No orphan staging document is created. Idempotent — a re-run
// against an already-committed item is a no-op, and the upsert itself is a
// same-bytes no-op on (tenant, external_key, checksum).
func (s *DocumentService) RouteCommit(ctx context.Context, tenantID, itemID uuid.UUID) (*RouteCommitResult, error) {
	item, err := s.RouteLoad(ctx, tenantID, itemID)
	if err != nil {
		return nil, err
	}
	if item.Status.IsTerminal() {
		// Already routed — report the prior mapping (idempotency).
		res := &RouteCommitResult{}
		if item.MatchDocumentID != nil {
			res.DocumentID = *item.MatchDocumentID
		}
		return res, nil
	}
	key := strings.TrimSpace(item.ExtractedExternalKey)
	if key == "" {
		return nil, vdmserr.Validation("extracted_external_key", "cannot commit without a business key")
	}
	if item.CreatedBy == nil || *item.CreatedBy == uuid.Nil {
		return nil, vdmserr.Validation("created_by", "staged item has no principal to commit as")
	}
	if item.FolderID == nil || *item.FolderID == uuid.Nil {
		return nil, vdmserr.Validation("folder_id", "staged item has no target folder")
	}

	// Replay as the original uploader with an admin role so the upsert's
	// folder/document edit gate passes for this system-initiated commit —
	// the same identity trick the signature seal consumer uses.
	actCtx := auth.WithUser(ctx, auth.UserInfo{TenantID: tenantID, ID: *item.CreatedBy, Role: "admin"})
	res, uerr := s.UpsertDocumentByExternalKey(actCtx, &UpsertByExternalKeyInput{
		ExternalID:    key,
		WorkspaceID:   item.WorkspaceID,
		FolderID:      *item.FolderID,
		DocumentClass: item.DocumentClass,
		RegionPin:     item.RegionPin,
		BlobChecksum:  item.BlobChecksum,
		BlobRef:       item.BlobRef.String(),
		Mime:          item.MimeType,
		ChangeSummary: "ingested via pre-commit pipeline",
		UpdatedBy:     *item.CreatedBy,
	})
	if uerr != nil {
		return nil, uerr
	}

	if terr := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		docID := res.DocumentID
		if e := s.repos.Ingestion.UpdateRouting(ctx, tx, tenantID, itemID, model.IngestCommitted, &docID); e != nil {
			return e
		}
		evt, eerr := model.NewOutboxEvent(tenantID, "dms.ingestion.routed.v1", "ingestion", itemID,
			model.IngestionRoutedPayload{
				IngestionItemID: itemID.String(),
				TenantID:        tenantID.String(),
				DocumentID:      res.DocumentID.String(),
				VersionID:       res.CurrentVersionID.String(),
				ExternalKey:     key,
				NewDocument:     res.Created,
			})
		if eerr != nil {
			return eerr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	}); terr != nil {
		return nil, terr
	}
	return &RouteCommitResult{
		DocumentID:  res.DocumentID,
		VersionID:   res.CurrentVersionID,
		NewDocument: res.Created,
	}, nil
}

// RouteNeedsReview parks a low-confidence / ambiguous staged item in the review
// queue and flips it to needs_review WITHOUT writing any version. candidate is
// the near-match the route step found, if any. Idempotent: a re-run that finds
// an existing review item is a no-op.
func (s *DocumentService) RouteNeedsReview(ctx context.Context, tenantID, itemID uuid.UUID, candidate *uuid.UUID, reason model.ReviewReason) (*model.ReviewQueueItem, error) {
	var out *model.ReviewQueueItem
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		item, e := s.repos.Ingestion.GetByID(ctx, tx, tenantID, itemID, true)
		if e != nil {
			return e
		}
		if item.Status.IsTerminal() {
			return nil // already routed; nothing to do
		}
		ri := &model.ReviewQueueItem{
			TenantID:                 tenantID,
			IngestionItemID:          itemID,
			WorkspaceID:              item.WorkspaceID,
			TargetCustomerRef:        item.TargetCustomerRef,
			DocumentClass:            item.DocumentClass,
			ExtractedExternalKey:     item.ExtractedExternalKey,
			SuggestedMatchDocumentID: candidate,
			Confidence:               item.Confidence,
			Reason:                   reason,
			Status:                   model.ReviewPending,
			BlobRef:                  item.BlobRef,
			BlobChecksum:             item.BlobChecksum,
			OCRResultRef:             item.OCRResultRef,
		}
		created, ce := s.repos.ReviewQueue.Create(ctx, tx, ri)
		if ce != nil {
			return ce
		}
		if !created {
			// Review already opened on a prior run — keep idempotent.
			out = ri
			return nil
		}
		if ue := s.repos.Ingestion.UpdateRouting(ctx, tx, tenantID, itemID, model.IngestNeedsReview, candidate); ue != nil {
			return ue
		}
		suggested := ""
		if candidate != nil {
			suggested = candidate.String()
		}
		evt, eerr := model.NewOutboxEvent(tenantID, "dms.review.created.v1", "review", ri.ID,
			model.ReviewCreatedPayload{
				ReviewID:                 ri.ID.String(),
				TenantID:                 tenantID.String(),
				IngestionItemID:          itemID.String(),
				Reason:                   string(reason),
				ExternalKey:              item.ExtractedExternalKey,
				SuggestedMatchDocumentID: suggested,
				Confidence:               item.Confidence,
			})
		if eerr != nil {
			return eerr
		}
		if oe := s.repos.Outbox.Insert(ctx, tx, evt); oe != nil {
			return oe
		}
		out = ri
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Read + resolve surface (REST) ----------------------------------------

// ListIngestionItems lists staged items, optionally filtered by status.
func (s *DocumentService) ListIngestionItems(ctx context.Context, status string, limit int) ([]model.IngestionItem, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.IngestionItem
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		x, e := s.repos.Ingestion.List(ctx, tx, tenantID, status, limit)
		out = x
		return e
	})
	return out, err
}

// ListReviewQueue lists review items, optionally filtered by status.
func (s *DocumentService) ListReviewQueue(ctx context.Context, status string, limit int) ([]model.ReviewQueueItem, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.ReviewQueueItem
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		x, e := s.repos.ReviewQueue.List(ctx, tx, tenantID, status, limit)
		out = x
		return e
	})
	return out, err
}

// ReviewItemDetail bundles a review item with its OCR text for the reviewer UI.
type ReviewItemDetail struct {
	Item    *model.ReviewQueueItem
	OCRText string
}

// GetReviewItem returns one review item plus its staged OCR text.
func (s *DocumentService) GetReviewItem(ctx context.Context, id uuid.UUID) (*ReviewItemDetail, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := &ReviewItemDetail{}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		it, e := s.repos.ReviewQueue.GetByID(ctx, tx, tenantID, id)
		if e != nil {
			return e
		}
		out.Item = it
		if it.OCRResultRef != nil {
			_ = tx.QueryRow(ctx,
				`SELECT full_text FROM ingestion_ocr WHERE tenant_id = $1 AND id = $2`,
				tenantID, *it.OCRResultRef).Scan(&out.OCRText)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveReviewInput is the reviewer's decision for a queued item (WS4).
type ResolveReviewInput struct {
	ReviewItemID     uuid.UUID
	Decision         string    // new_version | new_document | reject
	TargetDocumentID uuid.UUID // required for new_version
	ExternalKey      string    // optional for new_document (defaults to extracted)
	Notes            string
}

// ResolveReviewResult reports the outcome of a resolution.
type ResolveReviewResult struct {
	Status     model.ReviewStatus
	DocumentID uuid.UUID
	VersionID  uuid.UUID
}

// ResolveReviewItem finishes the routing of a queued item as a human decision:
//   - reject       → discard; no version written
//   - new_version  → append the staged blob as a new version of TargetDocumentID
//   - new_document → create a new document (+ v1) from the staged blob
//
// Runs as the reviewer (real permission checks apply via CreateVersion /
// CreateDocument). Emits dms.review.resolved.v1 on every outcome. Idempotent:
// resolving an already-resolved item returns a Conflict.
func (s *DocumentService) ResolveReviewItem(ctx context.Context, in *ResolveReviewInput) (*ResolveReviewResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if userID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}

	// Load the review + its staged item up front (own tx) so the commit paths
	// can reuse the standard CreateVersion / CreateDocument service methods
	// (each opens its own tenant tx).
	var (
		review *model.ReviewQueueItem
		item   *model.IngestionItem
	)
	if lerr := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r, e := s.repos.ReviewQueue.GetByID(ctx, tx, tenantID, in.ReviewItemID)
		if e != nil {
			return e
		}
		review = r
		it, e := s.repos.Ingestion.GetByID(ctx, tx, tenantID, r.IngestionItemID, false)
		if e != nil {
			return e
		}
		item = it
		return nil
	}); lerr != nil {
		return nil, lerr
	}
	if review.Status != model.ReviewPending {
		return nil, vdmserr.Conflict("review item already resolved")
	}

	res := &ResolveReviewResult{}
	switch in.Decision {
	case "reject":
		if terr := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			if e := s.repos.ReviewQueue.Resolve(ctx, tx, tenantID, in.ReviewItemID, model.ReviewRejected, "rejected", nil, nil, userID, in.Notes); e != nil {
				return e
			}
			if e := s.repos.Ingestion.SetStatus(ctx, tx, tenantID, item.ID, model.IngestRejected, "rejected in review"); e != nil {
				return e
			}
			return s.emitReviewResolved(ctx, tx, tenantID, in.ReviewItemID, item.ID, "reject", uuid.Nil, uuid.Nil, userID)
		}); terr != nil {
			return nil, terr
		}
		res.Status = model.ReviewRejected
		return res, nil

	case "new_version":
		if in.TargetDocumentID == uuid.Nil {
			return nil, errInvalidInput("target_document_id", "required for new_version")
		}
		v, cerr := s.CreateVersion(ctx, &CreateVersionInput{
			DocumentID:    in.TargetDocumentID,
			ContentBlobID: item.BlobRef,
			ChangeSummary: "committed from review queue",
		})
		if cerr != nil {
			return nil, cerr
		}
		res.DocumentID, res.VersionID = in.TargetDocumentID, v.ID
		if ferr := s.finishReviewCommit(ctx, tenantID, in.ReviewItemID, item.ID, "new_version", in.TargetDocumentID, v.ID, userID, in.Notes); ferr != nil {
			return nil, ferr
		}
		res.Status = model.ReviewResolved
		return res, nil

	case "new_document":
		key := strings.TrimSpace(in.ExternalKey)
		if key == "" {
			key = strings.TrimSpace(item.ExtractedExternalKey)
		}
		title := key
		if title == "" {
			title = "Ingested " + item.ID.String()
		}
		folder := uuid.Nil
		if item.FolderID != nil {
			folder = *item.FolderID
		}
		doc, derr := s.CreateDocument(ctx, &CreateDocumentInput{
			WorkspaceID: item.WorkspaceID,
			FolderID:    folder,
			Title:       title,
			RegionPin:   item.RegionPin,
			UpdatedBy:   userID,
			ExternalID:  key,
		})
		if derr != nil {
			return nil, derr
		}
		v, verr := s.CreateVersion(ctx, &CreateVersionInput{
			DocumentID:    doc.ID,
			ContentBlobID: item.BlobRef,
			ChangeSummary: "committed from ingestion review queue",
		})
		if verr != nil {
			return nil, verr
		}
		res.DocumentID, res.VersionID = doc.ID, v.ID
		if ferr := s.finishReviewCommit(ctx, tenantID, in.ReviewItemID, item.ID, "new_document", doc.ID, v.ID, userID, in.Notes); ferr != nil {
			return nil, ferr
		}
		res.Status = model.ReviewResolved
		return res, nil

	default:
		return nil, errInvalidInput("decision", "must be one of new_version, new_document, reject")
	}
}

// finishReviewCommit marks the review resolved + the staged item committed in
// one tx, after the version write has succeeded. decision is new_version |
// new_document. Emits dms.ingestion.routed.v1 (routing lifecycle) and
// dms.review.resolved.v1 (review lifecycle).
func (s *DocumentService) finishReviewCommit(ctx context.Context, tenantID, reviewID, itemID uuid.UUID, decision string, docID, verID, userID uuid.UUID, notes string) error {
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		d, v := docID, verID
		// resolution column mirrors the decision verb for audit.
		if e := s.repos.ReviewQueue.Resolve(ctx, tx, tenantID, reviewID, model.ReviewResolved, decision, &d, &v, userID, notes); e != nil {
			return e
		}
		if e := s.repos.Ingestion.UpdateRouting(ctx, tx, tenantID, itemID, model.IngestCommitted, &d); e != nil {
			return e
		}
		evt, eerr := model.NewOutboxEvent(tenantID, "dms.ingestion.routed.v1", "ingestion", itemID,
			model.IngestionRoutedPayload{
				IngestionItemID: itemID.String(),
				TenantID:        tenantID.String(),
				DocumentID:      docID.String(),
				VersionID:       verID.String(),
				NewDocument:     decision == "new_document",
			})
		if eerr != nil {
			return eerr
		}
		if oe := s.repos.Outbox.Insert(ctx, tx, evt); oe != nil {
			return oe
		}
		return s.emitReviewResolved(ctx, tx, tenantID, reviewID, itemID, decision, docID, verID, userID)
	})
}

// emitReviewResolved inserts the dms.review.resolved.v1 outbox event. docID/verID
// may be uuid.Nil for a reject.
func (s *DocumentService) emitReviewResolved(ctx context.Context, tx pgx.Tx, tenantID, reviewID, itemID uuid.UUID, decision string, docID, verID, userID uuid.UUID) error {
	p := model.ReviewResolvedPayload{
		ReviewID:        reviewID.String(),
		TenantID:        tenantID.String(),
		IngestionItemID: itemID.String(),
		Decision:        decision,
		ResolvedBy:      userID.String(),
	}
	if docID != uuid.Nil {
		p.DocumentID = docID.String()
	}
	if verID != uuid.Nil {
		p.VersionID = verID.String()
	}
	evt, err := model.NewOutboxEvent(tenantID, "dms.review.resolved.v1", "review", reviewID, p)
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

// ListReviewQueueKeyset is the keyset-paginated read behind
// GET /api/v1/review-queue. cursorTime zero = first page.
func (s *DocumentService) ListReviewQueueKeyset(ctx context.Context, status string, cursorTime time.Time, cursorID uuid.UUID, limit int) ([]model.ReviewQueueItem, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.ReviewQueueItem
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		x, e := s.repos.ReviewQueue.ListKeyset(ctx, tx, tenantID, status, cursorTime, cursorID, limit)
		out = x
		return e
	})
	return out, err
}
