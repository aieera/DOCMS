package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// Sync delta + per-device state (§3/§5). Delta is tenant/workspace-scoped and
// keyset-paginated; device ops are scoped to the caller's user.

type SyncDeltaResult struct {
	Changes    []model.SyncChange
	NextCursor string
	HasMore    bool
}

// SyncDelta returns the changes (upserts + tombstones) since cursor, ordered by
// (updated_at, id). workspace_id is REQUIRED and the caller must hold "view" on
// it — the metadata feed (titles/paths) must not leak workspaces the caller
// can't see. When deviceID is supplied it is enforced (a revoked device is
// rejected) and its server-side cursor + last-seen are advanced.
//
// Within the workspace the feed is filtered by the caller's reader set (D-8):
// tenant owner/admin see every row; anyone else sees shared folders, private
// folders they own or are granted (directly or via a group), and only the
// documents filed in those — so titles/paths of private folders never reach a
// member who cannot open them. Each page is then filtered per document
// through the policy (ACL + classification/clearance gate), see
// filterSyncDocuments. The feed carries no content hash (D-1).
func (s *DocumentService) SyncDelta(ctx context.Context, workspaceID, deviceID *uuid.UUID, cursor string, limit int) (*SyncDeltaResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if workspaceID == nil {
		return nil, vdmserr.Validation("workspace_id", "required")
	}
	// Workspace-view gate — mirrors the normal document/folder list paths so a
	// non-member can't enumerate an arbitrary workspace's tree.
	if err := s.requirePermission(ctx, userID, "view", "workspace", *workspaceID, map[string]any{
		"workspace_id": workspaceID.String(),
	}); err != nil {
		return nil, err
	}
	if deviceID != nil {
		err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			dev, e := s.repos.Sync.GetDevice(ctx, tx, tenantID, *deviceID)
			if e != nil {
				return e
			}
			if dev == nil || dev.UserID != userID {
				return vdmserr.NotFound("device not found")
			}
			if dev.RevokedAt != nil {
				return vdmserr.Forbidden("sync device has been revoked")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	reader := repository.SyncReader{
		UserID: userID,
		Groups: auth.GetUserGroups(ctx),
		All:    s.callerIsTenantAdmin(ctx),
	}
	gate, err := s.loadClassGate(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}

	// Per-document view filtering happens after the page fetch (it needs
	// the policy service), so a page can come back empty. The cursor still
	// advances past the whole fetched page, which keeps keyset pagination
	// exact; to avoid handing the client a run of empty pages we fetch up to
	// syncMaxFilteredPages pages before returning an empty page with
	// HasMore=true (the client simply continues from NextCursor).
	var res SyncDeltaResult
	cur := cursor
	for i := 0; i < syncMaxFilteredPages; i++ {
		var (
			changes []model.SyncChange
			next    string
			more    bool
			sens    map[uuid.UUID]repository.SyncDocSensitivity
		)
		err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			var e error
			changes, next, more, e = s.repos.Sync.Delta(ctx, tx, tenantID, workspaceID, cur, limit, reader)
			if e != nil {
				return e
			}
			var ids []uuid.UUID
			for _, c := range changes {
				if c.Kind == "document" {
					ids = append(ids, c.ID)
				}
			}
			sens, e = s.repos.Sync.DocSensitivity(ctx, tx, tenantID, ids)
			return e
		})
		if err != nil {
			return nil, err
		}
		filtered, ferr := s.filterSyncDocuments(ctx, userID, *workspaceID, changes, sens, gate)
		if ferr != nil {
			// Do NOT advance or persist the cursor: the hidden documents
			// would otherwise be skipped forever by this client.
			return nil, ferr
		}
		res.Changes = filtered
		res.NextCursor, res.HasMore = next, more
		cur = next
		if len(res.Changes) > 0 || !more {
			break
		}
	}

	// Advance the device's server-tracked position + last-seen.
	if deviceID != nil {
		err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
			return s.repos.Sync.UpdateCursor(ctx, tx, tenantID, *deviceID, res.NextCursor)
		})
		if err != nil {
			return nil, err
		}
	}
	return &res, nil
}

// ErrPolicyUnavailable: the per-document view check could not be evaluated
// (policy service error). SyncDelta fails the call without advancing the
// cursor; the sync handler answers 503 with Retry-After.
var ErrPolicyUnavailable = errors.New("policy service unavailable")

// syncMaxFilteredPages bounds how many fully-filtered pages one SyncDelta
// call will skip before returning an empty page (HasMore=true).
const syncMaxFilteredPages = 10

// filterSyncDocuments applies the per-document "view" decision (D-8) to a
// delta page: one BatchCheckPermission over the page's documents, with the
// workspace + role context the policy's document rules need and — when the
// tenant's classification gate is on — the clearance context
// enforceClassificationView uses, so ACL denials and clearance blocks both
// drop the row. Upserts the caller cannot view are removed; tombstones are
// kept (a client may hold a copy to delete) but reduced to the id. Folders
// were already filtered by the reader set in SQL. If the decision cannot be
// made (policy error, context build failure) it returns ErrPolicyUnavailable
// instead of guessing, so the caller fails the request without moving the
// cursor; denials are not errors.
func (s *DocumentService) filterSyncDocuments(ctx context.Context, userID, workspaceID uuid.UUID, changes []model.SyncChange, sens map[uuid.UUID]repository.SyncDocSensitivity, gate *classGate) ([]model.SyncChange, error) {
	var docIdx []int
	for i, c := range changes {
		if c.Kind == "document" {
			docIdx = append(docIdx, i)
		}
	}
	allowed := make(map[uuid.UUID]bool, len(docIdx))
	if len(docIdx) > 0 {
		role := auth.GetUserRole(ctx)
		checks := make([]*sedocv1.CheckPermissionRequest, 0, len(docIdx))
		for _, i := range docIdx {
			c := changes[i]
			extra := map[string]any{"workspace_id": workspaceID.String()}
			if role != "" {
				extra["user_role"] = role
			}
			sv := sens[c.ID]
			if gc := gate.contextFor(&model.Document{
				SecurityClassification: sv.SecurityClassification, HasPHI: sv.HasPHI, HasPII: sv.HasPII,
			}, "view"); gc != nil {
				for k, v := range gc {
					extra[k] = v
				}
			}
			st, err := structpb.NewStruct(stringifyMap(extra))
			if err != nil {
				return nil, fmt.Errorf("%w: build policy context: %v", ErrPolicyUnavailable, err)
			}
			checks = append(checks, &sedocv1.CheckPermissionRequest{
				SubjectType: "user", SubjectId: userID.String(), Action: "view",
				ResourceType: "document", ResourceId: c.ID.String(), Context: st,
			})
		}
		pairs := []string{"x-user-id", userID.String()}
		if tid, terr := auth.GetTenantID(ctx); terr == nil && tid != uuid.Nil {
			pairs = append(pairs, "x-tenant-id", tid.String())
		}
		if name := auth.GetUserName(ctx); name != "" {
			pairs = append(pairs, "x-user-name", name)
		}
		if role != "" {
			pairs = append(pairs, "x-user-role", role)
		}
		resp, err := s.policy.BatchCheckPermission(metadata.AppendToOutgoingContext(ctx, pairs...),
			&sedocv1.BatchCheckPermissionRequest{Checks: checks})
		if err != nil {
			s.log.Warn().Err(err).Msg("sync delta: policy batch check unavailable; failing the call without advancing the cursor")
			return nil, fmt.Errorf("%w: %v", ErrPolicyUnavailable, err)
		}
		if len(resp.GetResults()) != len(checks) {
			return nil, fmt.Errorf("%w: %d results for %d checks", ErrPolicyUnavailable, len(resp.GetResults()), len(checks))
		}
		for i, r := range resp.GetResults() {
			if r.GetAllowed() {
				if id, perr := uuid.Parse(checks[i].GetResourceId()); perr == nil {
					allowed[id] = true
				}
			}
		}
	}

	out := make([]model.SyncChange, 0, len(changes))
	for _, c := range changes {
		if c.Kind != "document" || allowed[c.ID] {
			out = append(out, c)
			continue
		}
		if c.Op == model.SyncOpDelete {
			out = append(out, model.SyncChange{Kind: c.Kind, Op: c.Op, ID: c.ID, UpdatedAt: c.UpdatedAt})
		}
	}
	return out, nil
}

func (s *DocumentService) RegisterSyncDevice(ctx context.Context, name, platform string, workspaceID *uuid.UUID) (*model.SyncDevice, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	d := model.SyncDevice{TenantID: tenantID, UserID: userID, Name: name, Platform: platform, WorkspaceID: workspaceID}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		id, e := s.repos.Sync.RegisterDevice(ctx, tx, d)
		if e != nil {
			return e
		}
		d.ID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentService) ListSyncDevices(ctx context.Context) ([]model.SyncDevice, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.SyncDevice
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Sync.ListDevices(ctx, tx, tenantID, userID)
		return err
	})
	return out, err
}

// getOwnedDevice loads a device and enforces caller ownership.
func (s *DocumentService) getOwnedDevice(ctx context.Context, tx pgx.Tx, tenantID, userID, deviceID uuid.UUID) (*model.SyncDevice, error) {
	d, err := s.repos.Sync.GetDevice(ctx, tx, tenantID, deviceID)
	if err != nil {
		return nil, err
	}
	if d == nil || d.UserID != userID {
		return nil, vdmserr.NotFound("device not found")
	}
	return d, nil
}

func (s *DocumentService) SaveSyncCursor(ctx context.Context, deviceID uuid.UUID, cursor string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		return s.repos.Sync.UpdateCursor(ctx, tx, tenantID, deviceID, cursor)
	})
}

func (s *DocumentService) SetSyncDeviceFolders(ctx context.Context, deviceID uuid.UUID, folderIDs []uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		return s.repos.Sync.SetSelectiveFolders(ctx, tx, tenantID, deviceID, folderIDs)
	})
}

func (s *DocumentService) RevokeSyncDevice(ctx context.Context, deviceID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		ok, e := s.repos.Sync.RevokeDevice(ctx, tx, tenantID, deviceID, userID)
		if e != nil {
			return e
		}
		if !ok {
			return vdmserr.NotFound("device not found or already revoked")
		}
		return nil
	})
}
