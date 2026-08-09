// Workspace templates (ADR 0118): CRUD + ProvisionFromTemplate.
//
// Provision scaffolds the template's folder tree (+ placeholder docs,
// metadata defaults, folder grants) into a workspace inside ONE tenant
// transaction — either the whole project structure exists afterwards
// or none of it does. Per-entity events (dms.folder.created.v1,
// dms.document.created.v1) are outbox-emitted in the same tx exactly
// as the one-at-a-time create paths do, so the search indexer and
// sync delta see provisioned entities identically to hand-created
// ones; dms.template.provisioned.v1 summarizes the run.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// requireTemplateAdmin gates template WRITES: templates are
// tenant-level shared assets, so authoring is admin/owner-only.
// Reads and provisioning are open to members (provision separately
// requires edit on the target workspace).
func requireTemplateAdmin(ctx context.Context) error {
	switch auth.GetUserRole(ctx) {
	case "admin", "owner":
		return nil
	default:
		return vdmserr.Forbidden("template management requires the admin or owner role")
	}
}

type TemplateInput struct {
	Name        string
	Description string
	Definition  json.RawMessage
}

func (s *DocumentService) CreateTemplate(ctx context.Context, in TemplateInput) (*model.WorkspaceTemplate, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errInvalidInput("name", "required")
	}
	if _, err := model.ParseTemplateDefinition(in.Definition); err != nil {
		return nil, vdmserr.Validation("definition", err.Error())
	}
	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	t := &model.WorkspaceTemplate{
		TenantID:    tenantID,
		ID:          id,
		Name:        strings.TrimSpace(in.Name),
		Description: in.Description,
		Definition:  in.Definition,
		CreatedBy:   userID,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return s.repos.Templates.Create(ctx, tx, t)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *DocumentService) UpdateTemplate(ctx context.Context, id uuid.UUID, in TemplateInput) (*model.WorkspaceTemplate, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireTemplateAdmin(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errInvalidInput("name", "required")
	}
	if _, err := model.ParseTemplateDefinition(in.Definition); err != nil {
		return nil, vdmserr.Validation("definition", err.Error())
	}
	var out *model.WorkspaceTemplate
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		t := &model.WorkspaceTemplate{
			TenantID:    tenantID,
			ID:          id,
			Name:        strings.TrimSpace(in.Name),
			Description: in.Description,
			Definition:  in.Definition,
		}
		ok, err := s.repos.Templates.Update(ctx, tx, t)
		if err != nil {
			return err
		}
		if !ok {
			return vdmserr.NotFound("template not found")
		}
		out, err = s.repos.Templates.GetByID(ctx, tx, tenantID, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DocumentService) GetTemplate(ctx context.Context, id uuid.UUID) (*model.WorkspaceTemplate, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *model.WorkspaceTemplate
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Templates.GetByID(ctx, tx, tenantID, id)
		return err
	})
	return out, err
}

func (s *DocumentService) ListTemplates(ctx context.Context) ([]model.WorkspaceTemplate, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.WorkspaceTemplate
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Templates.List(ctx, tx, tenantID)
		return err
	})
	return out, err
}

func (s *DocumentService) DeleteTemplate(ctx context.Context, id uuid.UUID) error {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := requireTemplateAdmin(ctx); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ok, err := s.repos.Templates.Delete(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if !ok {
			return vdmserr.NotFound("template not found")
		}
		return nil
	})
}

// ---- provisioning ----------------------------------------------------

type ProvisionInput struct {
	TemplateID     uuid.UUID
	WorkspaceID    uuid.UUID
	ParentFolderID *uuid.UUID
	Variables      map[string]string
	// IdempotencyKey, when non-empty, makes provisioning replay-safe: the
	// first call under a (tenant, key) stores its ProvisionResult and any
	// retry with the same key replays it instead of scaffolding a duplicate
	// tree. Empty = no idempotency (back-compat: every call provisions).
	IdempotencyKey string
}

// errProvisionKeyRace is an internal sentinel: a concurrent provision under
// the same idempotency key won the unique-key INSERT, so this call must roll
// back its (duplicate) tree and replay the winner's result. Never surfaced.
var errProvisionKeyRace = errors.New("provision idempotency key race")

// provisionDigest is a stable fingerprint of the request inputs. A retry with
// the same key but a DIFFERENT request must be rejected rather than replaying
// an unrelated result, so we compare digests on lookup. json.Marshal sorts map
// keys, so the Variables map encodes deterministically.
func provisionDigest(in ProvisionInput) string {
	parent := ""
	if in.ParentFolderID != nil {
		parent = in.ParentFolderID.String()
	}
	canon, _ := json.Marshal(struct {
		Template  string            `json:"template"`
		Workspace string            `json:"workspace"`
		Parent    string            `json:"parent"`
		Variables map[string]string `json:"variables"`
	}{in.TemplateID.String(), in.WorkspaceID.String(), parent, in.Variables})
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:])
}

// lookupProvision returns a stored ProvisionResult for (tenant, key), or nil
// when none exists. digest mismatch → Conflict (key reused for a different
// request). Runs in its own short tenant tx.
func (s *DocumentService) lookupProvision(ctx context.Context, tenantID uuid.UUID, key, digest string) (*ProvisionResult, error) {
	var out *ProvisionResult
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rec, err := s.repos.Templates.GetProvisionRecord(ctx, tx, tenantID, key)
		if err != nil {
			return err
		}
		if rec == nil {
			return nil
		}
		if digest != "" && rec.Digest != digest {
			return vdmserr.Conflict("idempotency key already used for a different provisioning request")
		}
		var res ProvisionResult
		if err := json.Unmarshal(rec.Result, &res); err != nil {
			return err
		}
		out = &res
		return nil
	})
	return out, err
}

type ProvisionResult struct {
	RootFolderIDs  []uuid.UUID `json:"root_folder_ids"`
	FoldersCreated int         `json:"folders_created"`
	DocsCreated    int         `json:"docs_created"`
}

// ProvisionFromTemplate scaffolds the template into the workspace.
// Requires edit on the workspace (same gate as CreateFolder). All
// variables referenced by the definition must be supplied.
func (s *DocumentService) ProvisionFromTemplate(ctx context.Context, in ProvisionInput) (*ProvisionResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if err := s.requirePermission(ctx, userID, "edit", "workspace", in.WorkspaceID, map[string]any{
		"workspace_id": in.WorkspaceID.String(),
	}); err != nil {
		return nil, err
	}

	digest := provisionDigest(in)

	// Idempotency fast path: a prior provision under this key replays its
	// stored result (no re-scaffolding). A digest mismatch is a Conflict.
	if in.IdempotencyKey != "" {
		if replay, err := s.lookupProvision(ctx, tenantID, in.IdempotencyKey, digest); err != nil {
			return nil, err
		} else if replay != nil {
			return replay, nil
		}
	}

	res := &ProvisionResult{}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tpl, err := s.repos.Templates.GetByID(ctx, tx, tenantID, in.TemplateID)
		if err != nil {
			return err
		}
		def, err := model.ParseTemplateDefinition(tpl.Definition)
		if err != nil {
			// A stored template failing to parse means the validation
			// contract changed underneath it — surface loudly.
			return vdmserr.Validation("definition", err.Error())
		}
		// All referenced variables must be supplied up front so the
		// tree can't come out half-substituted.
		var missing []string
		for _, v := range def.Variables() {
			if _, ok := in.Variables[v]; !ok {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			return vdmserr.Validation("variables", "missing: "+strings.Join(missing, ", "))
		}

		// Anchor path/depth: either the workspace root or an existing
		// parent folder (which must belong to the same workspace).
		baseDepth := -1 // roots get depth 0
		basePath := ""
		if in.ParentFolderID != nil {
			parent, err := s.repos.Folders.GetByID(ctx, tx, tenantID, *in.ParentFolderID)
			if err != nil {
				return err
			}
			if parent.WorkspaceID != in.WorkspaceID {
				return vdmserr.Validation("parent_folder_id", "parent is in a different workspace")
			}
			baseDepth = parent.Depth
			basePath = parent.Path
		}

		// Tenant metadata schema — placeholder docs validate against it
		// exactly like hand-created documents.
		schemaJSON, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		var walk func(n *model.TemplateNode, parentID *uuid.UUID, parentPath string, depth int, inherited map[string]string) (uuid.UUID, error)
		walk = func(n *model.TemplateNode, parentID *uuid.UUID, parentPath string, depth int, inherited map[string]string) (uuid.UUID, error) {
			if depth >= maxFolderDepth {
				return uuid.Nil, vdmserr.Validation("definition", "provisioned tree exceeds max folder depth at this location")
			}
			name, err := model.SubstituteTemplateVars(n.Name, in.Variables)
			if err != nil {
				return uuid.Nil, vdmserr.Validation("variables", err.Error())
			}
			if err := validateFolderName(name); err != nil {
				return uuid.Nil, err
			}
			id, err := newExternalID()
			if err != nil {
				return uuid.Nil, err
			}
			visibility := model.FolderVisibility(n.Visibility)
			if visibility == "" {
				visibility = model.FolderShared
			}
			folder := &model.Folder{
				TenantID:       tenantID,
				ID:             id,
				WorkspaceID:    in.WorkspaceID,
				ParentFolderID: parentID,
				Name:           name,
				Visibility:     visibility,
				CreatedBy:      userID,
				CreatedAt:      now,
				UpdatedAt:      now,
				Depth:          depth,
			}
			if visibility == model.FolderPrivate {
				u := userID
				folder.OwnerID = &u
			}
			if parentPath == "" {
				folder.Path = ltreeLabel(name, id)
			} else {
				folder.Path = parentPath + "." + ltreeLabel(name, id)
			}
			if err := s.repos.Folders.Create(ctx, tx, folder); err != nil {
				return uuid.Nil, err
			}
			res.FoldersCreated++

			// Folder-node metadata has no folder column to land in — it
			// is the DEFAULT set inherited by every placeholder document
			// at this node and below (deeper nodes/doc metadata override
			// key-by-key).
			if len(n.Metadata) > 0 {
				merged := make(map[string]string, len(inherited)+len(n.Metadata))
				for k, v := range inherited {
					merged[k] = v
				}
				for k, v := range n.Metadata {
					sv, err := model.SubstituteTemplateVars(v, in.Variables)
					if err != nil {
						return uuid.Nil, vdmserr.Validation("variables", err.Error())
					}
					merged[k] = sv
				}
				inherited = merged
			}

			// Same event the one-at-a-time path emits — indexer/sync
			// treat provisioned folders identically.
			evt, err := model.NewOutboxEvent(tenantID, "dms.folder.created.v1", "folder", folder.ID,
				model.FolderCreatedPayload{
					FolderID:    folder.ID.String(),
					WorkspaceID: folder.WorkspaceID.String(),
					Path:        folder.Path,
					Name:        folder.Name,
					CreatedBy:   userID.String(),
				})
			if err != nil {
				return uuid.Nil, err
			}
			if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
				return uuid.Nil, err
			}

			for _, g := range n.Grants {
				granteeID, _ := uuid.Parse(g.GranteeID) // validated at save
				gb := userID
				if err := s.repos.Folders.AddGrant(ctx, tx, &model.FolderGrant{
					ID:          uuid.New(),
					TenantID:    tenantID,
					FolderID:    folder.ID,
					GranteeType: g.GranteeType,
					GranteeID:   granteeID,
					GrantedBy:   &gb,
					CreatedAt:   now,
				}); err != nil {
					return uuid.Nil, err
				}
			}

			for _, dn := range n.Docs {
				if err := s.provisionDoc(ctx, tx, tenantID, userID, in.WorkspaceID, folder.ID, dn, in.Variables, inherited, schemaJSON, now); err != nil {
					return uuid.Nil, err
				}
				res.DocsCreated++
			}

			for i := range n.Children {
				if _, err := walk(&n.Children[i], &folder.ID, folder.Path, depth+1, inherited); err != nil {
					return uuid.Nil, err
				}
			}
			return folder.ID, nil
		}

		for i := range def.Nodes {
			rootID, err := walk(&def.Nodes[i], in.ParentFolderID, basePath, baseDepth+1, nil)
			if err != nil {
				return err
			}
			res.RootFolderIDs = append(res.RootFolderIDs, rootID)
		}

		// Summary event for downstream automation (audit, connectors).
		rootIDs := make([]string, 0, len(res.RootFolderIDs))
		for _, id := range res.RootFolderIDs {
			rootIDs = append(rootIDs, id.String())
		}
		payload := model.TemplateProvisionedPayload{
			TemplateID:     tpl.ID.String(),
			TemplateName:   tpl.Name,
			WorkspaceID:    in.WorkspaceID.String(),
			RootFolderIDs:  rootIDs,
			FoldersCreated: res.FoldersCreated,
			DocsCreated:    res.DocsCreated,
			Variables:      in.Variables,
			ProvisionedBy:  userID.String(),
		}
		if in.ParentFolderID != nil {
			payload.ParentFolderID = in.ParentFolderID.String()
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.template.provisioned.v1", "template", tpl.ID, payload)
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}

		// Claim the idempotency key + persist the result in the SAME tx as
		// the provisioned tree, so "scaffold AND record" commit atomically.
		// A concurrent duplicate loses the unique-key INSERT and must roll
		// back its tree to replay the winner's result.
		if in.IdempotencyKey != "" {
			resultJSON, err := json.Marshal(res)
			if err != nil {
				return err
			}
			inserted, err := s.repos.Templates.SaveProvisionRecord(ctx, tx, tenantID, in.IdempotencyKey, digest, resultJSON)
			if err != nil {
				return err
			}
			if !inserted {
				return errProvisionKeyRace
			}
		}
		return nil
	})
	if err != nil {
		// Lost the idempotency-key race: the winner committed its tree +
		// record; replay it instead of returning an error.
		if errors.Is(err, errProvisionKeyRace) {
			if replay, lerr := s.lookupProvision(ctx, tenantID, in.IdempotencyKey, digest); lerr == nil && replay != nil {
				return replay, nil
			}
		}
		return nil, err
	}
	return res, nil
}

// provisionDoc creates one placeholder document inside the provision
// tx, mirroring CreateDocument's row + event shape (metadata validated
// against the tenant schema; readable_by materialized best-effort).
func (s *DocumentService) provisionDoc(ctx context.Context, tx pgx.Tx, tenantID, userID, workspaceID, folderID uuid.UUID, dn model.TemplateDoc, vars map[string]string, inherited map[string]string, schemaJSON []byte, now time.Time) error {
	title, err := model.SubstituteTemplateVars(dn.Title, vars)
	if err != nil {
		return vdmserr.Validation("variables", err.Error())
	}
	// Folder-node metadata defaults first (already substituted), then
	// the doc's own metadata overrides key-by-key.
	meta := map[string]any{}
	for k, v := range inherited {
		meta[k] = v
	}
	for k, v := range dn.Metadata {
		sv, err := model.SubstituteTemplateVars(v, vars)
		if err != nil {
			return vdmserr.Validation("variables", err.Error())
		}
		meta[k] = sv
	}
	if err := validateMetadataAgainstSchema(schemaJSON, meta); err != nil {
		return err
	}
	tags := make([]string, 0, len(dn.Tags))
	for _, t := range dn.Tags {
		st, err := model.SubstituteTemplateVars(t, vars)
		if err != nil {
			return vdmserr.Validation("variables", err.Error())
		}
		tags = append(tags, st)
	}
	id, err := newExternalID()
	if err != nil {
		return err
	}
	doc := &model.Document{
		TenantID:       tenantID,
		ID:             id,
		WorkspaceID:    workspaceID,
		FolderID:       folderID,
		Title:          title,
		LifecycleState: model.StateDraft,
		CustomMetadata: meta,
		Tags:           tags,
		DocType:        model.DocTypeFile,
		CreatedBy:      userID,
		CreatedAt:      now,
		UpdatedBy:      userID, // documents_updated_by_fkey rejects the zero UUID
		UpdatedAt:      now,
	}
	if err := s.repos.Documents.Create(ctx, tx, doc); err != nil {
		return err
	}
	readableBy, readableUsers, readableGroups, rerr := s.computeFolderReaders(ctx, tx, tenantID, folderID, workspaceID)
	if rerr != nil {
		s.log.Warn().Err(rerr).Str("doc", doc.ID.String()).Msg("provision: compute readable_by failed; doc indexed without ACL")
		readableBy, readableUsers, readableGroups = nil, nil, nil
	}
	evt, err := model.NewOutboxEvent(tenantID, "dms.document.created.v1", "document", doc.ID,
		model.DocumentCreatedPayload{
			DocumentID:  doc.ID.String(),
			WorkspaceID: workspaceID.String(),
			FolderID:    folderID.String(),
			Title:       title,
			// Same projection as CreateDocument: without the dates the
			// search index stored Go's zero time for provisioned docs.
			LifecycleState:   string(doc.LifecycleState),
			CreatedAt:        doc.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:        doc.UpdatedAt.UTC().Format(time.RFC3339),
			CreatedBy:        userID.String(),
			ReadableBy:       readableBy,
			ReadableByUsers:  readableUsers,
			ReadableByGroups: readableGroups,
		})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}
