package bulk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// Service orchestrates streaming bulk import + export. Per ADR 0075
// it processes one batch at a time, ack-by-ack, with per-item
// failure granularity and external-id-based idempotency.
type Service struct {
	pool       *pgxpool.Pool
	repos      *repository.Repositories
	bulkRepo   *Repo
	authClient sedocv1.AuthServiceClient // optional; nil → user/group bulk returns ErrNotConfigured
	log        zerolog.Logger
	// concurrency bounds the document worker pool (Workstream 7). 0 → default.
	concurrency int
}

// defaultImportConcurrency bounds parallel document processing per batch. Kept
// modest so a backfill can't exhaust the DB connection pool (each item opens its
// own tx).
const defaultImportConcurrency = 8

func NewService(pool *pgxpool.Pool, repos *repository.Repositories, bulkRepo *Repo, authClient sedocv1.AuthServiceClient, log zerolog.Logger) *Service {
	return &Service{pool: pool, repos: repos, bulkRepo: bulkRepo, authClient: authClient, log: log}
}

// SetConcurrency overrides the document worker-pool size (≤0 ignored). Wired
// from SEDOC_BULK_IMPORT_CONCURRENCY in main.go.
func (s *Service) SetConcurrency(n int) {
	if n > 0 {
		s.concurrency = n
	}
}

func (s *Service) importConcurrency() int {
	if s.concurrency > 0 {
		return s.concurrency
	}
	return defaultImportConcurrency
}

// ProcessBatch handles one BulkImportRequest. Returns per-item
// results, the aggregate status, and the response payload to
// persist for replay. Items that fail individually don't fail the
// whole batch — the caller surfaces success_count / failure_count
// to the client.
func (s *Service) ProcessBatch(ctx context.Context, tenantID uuid.UUID, req *sedocv1.BulkImportRequest) (*sedocv1.BulkImportResponse, error) {
	requestID, err := uuid.Parse(req.GetRequestId())
	if err != nil {
		return nil, fmt.Errorf("request_id: %w", err)
	}

	// Idempotency replay path. Same (tenant, request_id) → return
	// the cached response.
	digest := DigestItems(req.GetItems())
	if existing, err := s.bulkRepo.LookupRequest(ctx, tenantID, requestID); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.ItemsDigest != digest {
			return nil, FmtDigestMismatch(requestID)
		}
		var cached sedocv1.BulkImportResponse
		if len(existing.ResponseJSON) > 0 {
			_ = json.Unmarshal(existing.ResponseJSON, &cached)
		}
		// Ensure the response_id is stamped even if older code
		// didn't include it.
		cached.RequestId = req.GetRequestId()
		return &cached, nil
	}

	// Two-phase processing (Workstream 7). Structural items
	// (workspace/folder/user/group) run SEQUENTIALLY in input order so
	// intra-batch dependencies hold (document→workspace/folder, folder→parent
	// folder) — these are few in a backfill. Documents then run through a
	// BOUNDED worker pool: they're mutually independent (their workspace/folder
	// already exist), and duplicate external_ids converge via InsertUpsert
	// rather than racing. Results are written by original index so order +
	// idempotency replay are unaffected.
	items := req.GetItems()
	results := make([]*sedocv1.BulkItemResult, len(items))
	docIdx := make([]int, 0, len(items))
	for i, item := range items {
		if _, isDoc := item.GetResource().(*sedocv1.BulkItem_Document); isDoc {
			docIdx = append(docIdx, i)
			continue
		}
		results[i] = s.processOne(ctx, tenantID, item)
	}
	if len(docIdx) > 0 {
		sem := make(chan struct{}, s.importConcurrency())
		var wg sync.WaitGroup
		for _, i := range docIdx {
			wg.Add(1)
			sem <- struct{}{} // bounded pool = throttle
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()
				results[idx] = s.processOne(ctx, tenantID, items[idx])
			}(i)
		}
		wg.Wait()
	}
	successCount, failureCount := 0, 0
	for _, res := range results {
		if res.Success {
			successCount++
		} else {
			failureCount++
		}
	}

	resp := &sedocv1.BulkImportResponse{
		RequestId: req.GetRequestId(),
		Results:   results,
	}
	respJSON, _ := json.Marshal(resp)

	status := "succeeded"
	switch {
	case failureCount > 0 && successCount > 0:
		status = "partial"
	case failureCount > 0 && successCount == 0:
		status = "failed"
	}

	_ = s.bulkRepo.SaveRequest(ctx, tenantID, &ImportLogStatus{
		RequestID:    requestID,
		ItemsDigest:  digest,
		Status:       status,
		ItemCount:    len(req.GetItems()),
		SuccessCount: successCount,
		FailureCount: failureCount,
		ResponseJSON: respJSON,
	})
	return resp, nil
}

// processOne dispatches one BulkItem to the right per-resource
// handler. Lives outside the loop body so each item gets its own
// transaction — a single bad row doesn't roll back its peers.
func (s *Service) processOne(ctx context.Context, tenantID uuid.UUID, item *sedocv1.BulkItem) *sedocv1.BulkItemResult {
	switch v := item.GetResource().(type) {
	case *sedocv1.BulkItem_Workspace:
		return s.processWorkspace(ctx, tenantID, v.Workspace)
	case *sedocv1.BulkItem_Folder:
		return s.processFolder(ctx, tenantID, v.Folder)
	case *sedocv1.BulkItem_Document:
		return s.processDocument(ctx, tenantID, v.Document)
	case *sedocv1.BulkItem_User:
		return s.processUser(ctx, tenantID, v.User)
	case *sedocv1.BulkItem_Group:
		return s.processGroup(ctx, tenantID, v.Group)
	default:
		return &sedocv1.BulkItemResult{Success: false, Error: "unknown resource kind"}
	}
}

// ---- Per-resource processors --------------------------------------------

func (s *Service) processWorkspace(ctx context.Context, tenantID uuid.UUID, w *sedocv1.BulkWorkspace) *sedocv1.BulkItemResult {
	if w.GetExternalId() == "" {
		return &sedocv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(w.GetName()) == "" {
		return &sedocv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: "name required"}
	}
	userID := callerUserID(ctx)

	var internalID uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Idempotent: external_id lookup short-circuits.
		existing, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceWorkspace, w.GetExternalId())
		if err != nil {
			return err
		}
		if existing != uuid.Nil {
			internalID = existing
			return nil
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		regionPin := w.GetRegionPin()
		if regionPin == "" {
			regionPin = "us-east-1"
		}
		ws := &model.Workspace{
			TenantID: tenantID, ID: id, Name: w.GetName(),
			Description: w.GetDescription(), RegionPin: regionPin,
			CreatedBy: userID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := s.repos.Workspaces.Create(ctx, tx, ws); err != nil {
			return err
		}
		// Auto-enroll the importing user as workspace admin so the
		// bulk caller can read its own writes immediately. Same
		// rationale as CreateWorkspace.
		if err := s.repos.Workspaces.AddMember(ctx, tx, tenantID, id, userID, userID, "admin"); err != nil {
			return err
		}
		// Auto-create the Root folder so subsequent BulkDocument items
		// can reference the workspace at the root level.
		rootID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		root := &model.Folder{
			TenantID: tenantID, ID: rootID, WorkspaceID: id, Name: "Root",
			Path: ltreeLabel("Root", rootID), Depth: 0,
			CreatedBy: userID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := s.repos.Folders.Create(ctx, tx, root); err != nil {
			return err
		}
		internalID = id
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceWorkspace, w.GetExternalId(), id)
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &sedocv1.BulkItemResult{
		ExternalId: w.GetExternalId(),
		Success:    true,
		InternalId: internalID.String(),
	}
}

func (s *Service) processFolder(ctx context.Context, tenantID uuid.UUID, f *sedocv1.BulkFolder) *sedocv1.BulkItemResult {
	if f.GetExternalId() == "" {
		return &sedocv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(f.GetName()) == "" {
		return &sedocv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: "name required"}
	}
	if f.GetWorkspaceExternalId() == "" {
		return &sedocv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: "workspace_external_id required"}
	}
	userID := callerUserID(ctx)

	var internalID uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if existing, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceFolder, f.GetExternalId()); err != nil {
			return err
		} else if existing != uuid.Nil {
			internalID = existing
			return nil
		}
		wsID, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceWorkspace, f.GetWorkspaceExternalId())
		if err != nil {
			return err
		}
		if wsID == uuid.Nil {
			return fmt.Errorf("workspace_external_id %q not found in this import", f.GetWorkspaceExternalId())
		}
		var parentID *uuid.UUID
		var parentPath string
		var parentDepth int
		if f.GetParentExternalId() != "" {
			pid, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceFolder, f.GetParentExternalId())
			if err != nil {
				return err
			}
			if pid == uuid.Nil {
				return fmt.Errorf("parent_external_id %q not found in this import", f.GetParentExternalId())
			}
			parentID = &pid
			// Pull the parent's ltree path + depth so this folder
			// nests correctly. Without these the row violates the
			// ltree path-construction invariant maintained elsewhere.
			if err := tx.QueryRow(ctx, `
				SELECT path::text, depth FROM folders
				WHERE tenant_id = $1 AND id = $2
			`, tenantID, pid).Scan(&parentPath, &parentDepth); err != nil {
				return fmt.Errorf("parent folder %s lookup: %w", pid, err)
			}
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		path := ltreeLabel(f.GetName(), id)
		depth := 0
		if parentID != nil {
			path = parentPath + "." + path
			depth = parentDepth + 1
		}
		folder := &model.Folder{
			TenantID: tenantID, ID: id, WorkspaceID: wsID, ParentFolderID: parentID,
			Name: f.GetName(), Path: path, Depth: depth,
			CreatedBy: userID, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := s.repos.Folders.Create(ctx, tx, folder); err != nil {
			return err
		}
		internalID = id
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceFolder, f.GetExternalId(), id)
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &sedocv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: true, InternalId: internalID.String()}
}

func (s *Service) processDocument(ctx context.Context, tenantID uuid.UUID, d *sedocv1.BulkDocument) *sedocv1.BulkItemResult {
	if d.GetExternalId() == "" {
		return &sedocv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(d.GetTitle()) == "" {
		return &sedocv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: "title required"}
	}
	if d.GetWorkspaceExternalId() == "" {
		return &sedocv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: "workspace_external_id required"}
	}
	userID := callerUserID(ctx)

	var internalID uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Existing doc? Check the first-class external_id column (shared
		// with the ERP upsert surface) first, then the legacy bulk map.
		// When found, append a version if a (different) blob is supplied —
		// bulk can now version by external key, not just dedupe creation.
		if existingID, eerr := s.resolveExistingDocByKey(ctx, tx, tenantID, d.GetExternalId()); eerr != nil {
			return eerr
		} else if existingID != uuid.Nil {
			internalID = existingID
			if blob := d.GetContentBlobId(); blob != "" {
				if blobID, perr := uuid.Parse(blob); perr == nil {
					if lerr := s.linkBlobVersion(ctx, tx, tenantID, userID, existingID, blobID); lerr != nil {
						return lerr
					}
				}
			}
			return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceDocument, d.GetExternalId(), existingID)
		}
		wsID, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceWorkspace, d.GetWorkspaceExternalId())
		if err != nil {
			return err
		}
		if wsID == uuid.Nil {
			return fmt.Errorf("workspace_external_id %q not found in this import", d.GetWorkspaceExternalId())
		}
		var folderID uuid.UUID
		if d.GetFolderExternalId() != "" {
			folderID, err = s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceFolder, d.GetFolderExternalId())
			if err != nil {
				return err
			}
			if folderID == uuid.Nil {
				return fmt.Errorf("folder_external_id %q not found in this import", d.GetFolderExternalId())
			}
		} else {
			// Default to the workspace's Root folder. The bulk
			// workspace creator auto-inserts one with name "Root".
			folderID, err = lookupRootFolder(ctx, tx, tenantID, wsID)
			if err != nil {
				return err
			}
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		regionPin := d.GetRegionPin()
		if regionPin == "" {
			regionPin = "us-east-1"
		}
		doc := &model.Document{
			TenantID: tenantID, ID: id, ExternalID: d.GetExternalId(),
			WorkspaceID: wsID, FolderID: folderID,
			Title: d.GetTitle(), Description: d.GetDescription(),
			LifecycleState: model.StateDraft, RegionPin: regionPin,
			Tags: d.GetTags(), CustomMetadata: map[string]any{},
			CreatedBy: userID, CreatedAt: time.Now().UTC(),
			UpdatedBy: userID, UpdatedAt: time.Now().UTC(),
		}
		// InsertUpsert (ON CONFLICT DO NOTHING on the tenant+external_id index)
		// instead of Create so two parallel workers carrying the SAME external_id
		// in one batch converge on one row instead of the loser hitting a unique
		// violation. created=false → another worker won the race; re-resolve its
		// id and fall through to the shared blob-link path.
		created, cerr := s.repos.Documents.InsertUpsert(ctx, tx, doc)
		if cerr != nil {
			return cerr
		}
		if !created {
			existingID, rerr := s.resolveExistingDocByKey(ctx, tx, tenantID, d.GetExternalId())
			if rerr != nil {
				return rerr
			}
			if existingID == uuid.Nil {
				return fmt.Errorf("external_id %q lost the create race but is not resolvable", d.GetExternalId())
			}
			id = existingID
		}
		// If a content_blob_id was supplied, link it as the next version with the
		// blob's real size/sha/mime + head repoint. linkBlobVersion is a no-op
		// when the head already references this blob (re-imports / converged race).
		if blob := d.GetContentBlobId(); blob != "" {
			if blobID, perr := uuid.Parse(blob); perr == nil {
				if lerr := s.linkBlobVersion(ctx, tx, tenantID, userID, id, blobID); lerr != nil {
					return lerr
				}
			}
		}
		internalID = id
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceDocument, d.GetExternalId(), id)
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &sedocv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: true, InternalId: internalID.String()}
}

// resolveExistingDocByKey finds the internal id for a document business key,
// preferring the first-class documents.external_id column (shared with the ERP
// upsert surface) and falling back to the legacy bulk_external_id_map shim for
// any pre-migration row the backfill missed. Returns uuid.Nil when unknown.
func (s *Service) resolveExistingDocByKey(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, externalID string) (uuid.UUID, error) {
	if doc, err := s.repos.Documents.GetByExternalID(ctx, tx, tenantID, externalID, false); err == nil {
		return doc.ID, nil
	} else if !errors.Is(err, vdmserr.ErrNotFound) {
		return uuid.Nil, err
	}
	return s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceDocument, externalID)
}

// linkBlobVersion appends blobID as the next version of docID and repoints the
// document head, deriving size/sha/mime from the content_blobs row. Idempotent
// per blob: a no-op when the head already references this blob (re-imports).
// Bulk import is event-light by design — no per-version dms.version.uploaded.v1
// (the ERP upsert endpoint is the event-emitting path) so a large import
// doesn't flood the intelligence pipeline.
func (s *Service) linkBlobVersion(ctx context.Context, tx pgx.Tx, tenantID, userID, docID, blobID uuid.UUID) error {
	latest, err := s.repos.Versions.GetLatestByDocument(ctx, tx, tenantID, docID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return err
	}
	if latest != nil && latest.ContentBlobID == blobID {
		return nil
	}
	var (
		size int64
		sha  string
		mime *string
	)
	if err := tx.QueryRow(ctx,
		`SELECT size_bytes, sha256_hash, mime_type FROM content_blobs WHERE tenant_id = $1 AND id = $2`,
		tenantID, blobID).Scan(&size, &sha, &mime); err != nil {
		return fmt.Errorf("lookup content_blob %s: %w", blobID, err)
	}
	n, err := s.repos.Versions.NextVersionNumber(ctx, tx, tenantID, docID)
	if err != nil {
		return err
	}
	mimeStr := ""
	if mime != nil {
		mimeStr = *mime
	}
	v := &model.Version{
		TenantID: tenantID, ID: mustUUIDv7(), DocumentID: docID, VersionNumber: n,
		ContentBlobID: blobID, SizeBytes: size, SHA256Hash: sha, MimeType: mimeStr,
		CreatedBy: userID, CreatedAt: time.Now().UTC(),
	}
	if err := s.repos.Versions.Create(ctx, tx, v); err != nil {
		return err
	}
	return s.repos.Documents.SetCurrentVersion(ctx, tx, tenantID, docID, v.ID, sha, mimeStr, size)
}

// processUser + processGroup dispatch outbound to the auth service
// via gRPC. The bulk_external_id_map row lives in document's DB so
// the cross-reference resolution stays uniform; auth doesn't need
// to know about bulk.
func (s *Service) processUser(ctx context.Context, tenantID uuid.UUID, u *sedocv1.BulkUser) *sedocv1.BulkItemResult {
	if u.GetExternalId() == "" {
		return &sedocv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if u.GetEmail() == "" {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "email required"}
	}
	if s.authClient == nil {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "auth service not configured"}
	}
	// Idempotent fast path.
	var existing uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		id, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceUser, u.GetExternalId())
		existing = id
		return err
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: err.Error()}
	}
	if existing != uuid.Nil {
		return &sedocv1.BulkItemResult{
			ExternalId: u.GetExternalId(), Success: true, InternalId: existing.String(), Skipped: true,
		}
	}
	resp, err := s.authClient.CreateUser(ctx, &sedocv1.CreateUserRequest{
		Email: u.GetEmail(), FullName: u.GetDisplayName(), Role: u.GetRole(),
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: err.Error()}
	}
	internalID, perr := uuid.Parse(resp.GetId())
	if perr != nil {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "auth returned bad uuid: " + resp.GetId()}
	}
	if rerr := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceUser, u.GetExternalId(), internalID)
	}); rerr != nil {
		return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: rerr.Error()}
	}
	return &sedocv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: true, InternalId: internalID.String()}
}

func (s *Service) processGroup(ctx context.Context, tenantID uuid.UUID, g *sedocv1.BulkGroup) *sedocv1.BulkItemResult {
	if g.GetExternalId() == "" {
		return &sedocv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(g.GetName()) == "" {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "name required"}
	}
	if s.authClient == nil {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "auth service not configured"}
	}
	var existing uuid.UUID
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		id, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceGroup, g.GetExternalId())
		existing = id
		return err
	}); err != nil {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: err.Error()}
	}
	if existing != uuid.Nil {
		return &sedocv1.BulkItemResult{
			ExternalId: g.GetExternalId(), Success: true, InternalId: existing.String(), Skipped: true,
		}
	}
	resp, err := s.authClient.CreateGroup(ctx, &sedocv1.CreateGroupRequest{
		Name: g.GetName(), Description: g.GetDescription(),
	})
	if err != nil {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: err.Error()}
	}
	internalID, perr := uuid.Parse(resp.GetId())
	if perr != nil {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "auth returned bad uuid: " + resp.GetId()}
	}
	if rerr := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceGroup, g.GetExternalId(), internalID)
	}); rerr != nil {
		return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: rerr.Error()}
	}
	return &sedocv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: true, InternalId: internalID.String()}
}

// ---- Helpers ----------------------------------------------------------------

// callerUserID pulls the caller's user UUID from context.UserInfo.
// Bulk processors run inside SessionAuth or gateway-trust paths
// upstream; if neither populated UserInfo we fall back to nil
// (the column is nullable). In practice the import is always run
// by an authenticated admin.
func callerUserID(ctx context.Context) uuid.UUID {
	u, err := auth.User(ctx)
	if err != nil {
		return uuid.Nil
	}
	return u.ID
}

func lookupRootFolder(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM folders
		WHERE tenant_id = $1 AND workspace_id = $2 AND parent_folder_id IS NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, tenantID, workspaceID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("root folder for workspace %s not found: %w", workspaceID, err)
	}
	return id, nil
}

func mustUUIDv7() uuid.UUID {
	id, _ := uuid.NewV7()
	return id
}

// ltreeLabel duplicates the helper in services/document/internal/service
// (unexported there). ltree allows only [A-Za-z0-9_]; slug the name
// and append a UUID prefix so labels stay unique even when names repeat.
func ltreeLabel(name string, id uuid.UUID) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('_')
		}
	}
	slug := b.String()
	if slug == "" {
		slug = "f"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	short := id.String()[:8]
	return slug + "_" + short
}
