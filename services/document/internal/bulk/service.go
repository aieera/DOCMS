package bulk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	vaultdmsv1 "github.com/vaultdms/vaultdms/proto/gen/go/vaultdms/v1"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// Service orchestrates streaming bulk import + export. Per ADR 0075
// it processes one batch at a time, ack-by-ack, with per-item
// failure granularity and external-id-based idempotency.
type Service struct {
	pool       *pgxpool.Pool
	repos      *repository.Repositories
	bulkRepo   *Repo
	authClient vaultdmsv1.AuthServiceClient // optional; nil → user/group bulk returns ErrNotConfigured
	log        zerolog.Logger
}

func NewService(pool *pgxpool.Pool, repos *repository.Repositories, bulkRepo *Repo, authClient vaultdmsv1.AuthServiceClient, log zerolog.Logger) *Service {
	return &Service{pool: pool, repos: repos, bulkRepo: bulkRepo, authClient: authClient, log: log}
}

// ProcessBatch handles one BulkImportRequest. Returns per-item
// results, the aggregate status, and the response payload to
// persist for replay. Items that fail individually don't fail the
// whole batch — the caller surfaces success_count / failure_count
// to the client.
func (s *Service) ProcessBatch(ctx context.Context, tenantID uuid.UUID, req *vaultdmsv1.BulkImportRequest) (*vaultdmsv1.BulkImportResponse, error) {
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
		var cached vaultdmsv1.BulkImportResponse
		if len(existing.ResponseJSON) > 0 {
			_ = json.Unmarshal(existing.ResponseJSON, &cached)
		}
		// Ensure the response_id is stamped even if older code
		// didn't include it.
		cached.RequestId = req.GetRequestId()
		return &cached, nil
	}

	results := make([]*vaultdmsv1.BulkItemResult, 0, len(req.GetItems()))
	successCount, failureCount := 0, 0
	for _, item := range req.GetItems() {
		res := s.processOne(ctx, tenantID, item)
		if res.Success {
			successCount++
		} else {
			failureCount++
		}
		results = append(results, res)
	}

	resp := &vaultdmsv1.BulkImportResponse{
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
func (s *Service) processOne(ctx context.Context, tenantID uuid.UUID, item *vaultdmsv1.BulkItem) *vaultdmsv1.BulkItemResult {
	switch v := item.GetResource().(type) {
	case *vaultdmsv1.BulkItem_Workspace:
		return s.processWorkspace(ctx, tenantID, v.Workspace)
	case *vaultdmsv1.BulkItem_Folder:
		return s.processFolder(ctx, tenantID, v.Folder)
	case *vaultdmsv1.BulkItem_Document:
		return s.processDocument(ctx, tenantID, v.Document)
	case *vaultdmsv1.BulkItem_User:
		return s.processUser(ctx, tenantID, v.User)
	case *vaultdmsv1.BulkItem_Group:
		return s.processGroup(ctx, tenantID, v.Group)
	default:
		return &vaultdmsv1.BulkItemResult{Success: false, Error: "unknown resource kind"}
	}
}

// ---- Per-resource processors --------------------------------------------

func (s *Service) processWorkspace(ctx context.Context, tenantID uuid.UUID, w *vaultdmsv1.BulkWorkspace) *vaultdmsv1.BulkItemResult {
	if w.GetExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(w.GetName()) == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: "name required"}
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
		return &vaultdmsv1.BulkItemResult{ExternalId: w.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &vaultdmsv1.BulkItemResult{
		ExternalId: w.GetExternalId(),
		Success:    true,
		InternalId: internalID.String(),
	}
}

func (s *Service) processFolder(ctx context.Context, tenantID uuid.UUID, f *vaultdmsv1.BulkFolder) *vaultdmsv1.BulkItemResult {
	if f.GetExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(f.GetName()) == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: "name required"}
	}
	if f.GetWorkspaceExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: "workspace_external_id required"}
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
		return &vaultdmsv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &vaultdmsv1.BulkItemResult{ExternalId: f.GetExternalId(), Success: true, InternalId: internalID.String()}
}

func (s *Service) processDocument(ctx context.Context, tenantID uuid.UUID, d *vaultdmsv1.BulkDocument) *vaultdmsv1.BulkItemResult {
	if d.GetExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(d.GetTitle()) == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: "title required"}
	}
	if d.GetWorkspaceExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: "workspace_external_id required"}
	}
	userID := callerUserID(ctx)

	var internalID uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if existing, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceDocument, d.GetExternalId()); err != nil {
			return err
		} else if existing != uuid.Nil {
			internalID = existing
			return nil
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
			TenantID: tenantID, ID: id, WorkspaceID: wsID, FolderID: folderID,
			Title: d.GetTitle(), Description: d.GetDescription(),
			LifecycleState: model.StateDraft, RegionPin: regionPin,
			Tags: d.GetTags(), CustomMetadata: map[string]any{},
			CreatedBy: userID, CreatedAt: time.Now().UTC(),
			UpdatedBy: userID, UpdatedAt: time.Now().UTC(),
		}
		if err := s.repos.Documents.Create(ctx, tx, doc); err != nil {
			return err
		}
		// If a content_blob_id was supplied, link it as version 1.
		// Skipped when empty — the caller can attach content later
		// via the regular CreateVersion flow.
		if blob := d.GetContentBlobId(); blob != "" {
			if blobID, perr := uuid.Parse(blob); perr == nil {
				_ = s.repos.Versions.Create(ctx, tx, &model.Version{
					TenantID: tenantID, ID: mustUUIDv7(), DocumentID: id,
					ContentBlobID: blobID, VersionNumber: 1,
					CreatedBy: userID, CreatedAt: time.Now().UTC(),
				})
			}
		}
		internalID = id
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceDocument, d.GetExternalId(), id)
	})
	if err != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: false, Error: err.Error()}
	}
	return &vaultdmsv1.BulkItemResult{ExternalId: d.GetExternalId(), Success: true, InternalId: internalID.String()}
}

// processUser + processGroup dispatch outbound to the auth service
// via gRPC. The bulk_external_id_map row lives in document's DB so
// the cross-reference resolution stays uniform; auth doesn't need
// to know about bulk.
func (s *Service) processUser(ctx context.Context, tenantID uuid.UUID, u *vaultdmsv1.BulkUser) *vaultdmsv1.BulkItemResult {
	if u.GetExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if u.GetEmail() == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "email required"}
	}
	if s.authClient == nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "auth service not configured"}
	}
	// Idempotent fast path.
	var existing uuid.UUID
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		id, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceUser, u.GetExternalId())
		existing = id
		return err
	})
	if err != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: err.Error()}
	}
	if existing != uuid.Nil {
		return &vaultdmsv1.BulkItemResult{
			ExternalId: u.GetExternalId(), Success: true, InternalId: existing.String(), Skipped: true,
		}
	}
	resp, err := s.authClient.CreateUser(ctx, &vaultdmsv1.CreateUserRequest{
		Email: u.GetEmail(), FullName: u.GetDisplayName(), Role: u.GetRole(),
	})
	if err != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: err.Error()}
	}
	internalID, perr := uuid.Parse(resp.GetId())
	if perr != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: "auth returned bad uuid: " + resp.GetId()}
	}
	if rerr := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceUser, u.GetExternalId(), internalID)
	}); rerr != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: false, Error: rerr.Error()}
	}
	return &vaultdmsv1.BulkItemResult{ExternalId: u.GetExternalId(), Success: true, InternalId: internalID.String()}
}

func (s *Service) processGroup(ctx context.Context, tenantID uuid.UUID, g *vaultdmsv1.BulkGroup) *vaultdmsv1.BulkItemResult {
	if g.GetExternalId() == "" {
		return &vaultdmsv1.BulkItemResult{Success: false, Error: "external_id required"}
	}
	if strings.TrimSpace(g.GetName()) == "" {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "name required"}
	}
	if s.authClient == nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "auth service not configured"}
	}
	var existing uuid.UUID
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		id, err := s.bulkRepo.LookupExternal(ctx, tx, tenantID, ResourceGroup, g.GetExternalId())
		existing = id
		return err
	}); err != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: err.Error()}
	}
	if existing != uuid.Nil {
		return &vaultdmsv1.BulkItemResult{
			ExternalId: g.GetExternalId(), Success: true, InternalId: existing.String(), Skipped: true,
		}
	}
	resp, err := s.authClient.CreateGroup(ctx, &vaultdmsv1.CreateGroupRequest{
		Name: g.GetName(), Description: g.GetDescription(),
	})
	if err != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: err.Error()}
	}
	internalID, perr := uuid.Parse(resp.GetId())
	if perr != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: "auth returned bad uuid: " + resp.GetId()}
	}
	if rerr := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return s.bulkRepo.RecordExternal(ctx, tx, tenantID, ResourceGroup, g.GetExternalId(), internalID)
	}); rerr != nil {
		return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: false, Error: rerr.Error()}
	}
	return &vaultdmsv1.BulkItemResult{ExternalId: g.GetExternalId(), Success: true, InternalId: internalID.String()}
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
