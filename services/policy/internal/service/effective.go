package service

import (
	"context"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/policy/internal/model"
)

// EffectivePrincipal is one user or group that can access a resource, with
// the highest capability it holds and the grant sources that confer it.
type EffectivePrincipal struct {
	PrincipalType model.PrincipalType
	PrincipalID   uuid.UUID
	Capability    model.Capability // highest across all reasons
	Reasons       []string         // e.g. "edit (direct)", "view (via workspace)"
}

// EffectiveAccess is the "who can see this" answer. Principals enumerates
// the concrete grant/admin-based accessors; the implicit-category fields
// describe broad access that is intentionally NOT enumerated per-user
// (org admins; every workspace member's baseline view) so the set stays
// bounded and the answer stays honest rather than under-reporting.
type EffectiveAccess struct {
	Principals []EffectivePrincipal
	// OrgAdminsHaveAccess is always true — org owners/admins have admin on
	// everything (Rego rule 6). Surfaced so the UI can say so explicitly.
	OrgAdminsHaveAccess bool
	// WorkspaceBaselineView, when set, means every member of that workspace
	// can at least view this resource (Rego rule 5a). Nil when the resource
	// sits in a private folder that overrides the workspace baseline.
	WorkspaceBaselineView *uuid.UUID
	// PrivateFolder is true when the resource is in (or is) a private folder.
	PrivateFolder bool
	// FolderOwner is the private-folder owner, who always has access.
	FolderOwner *uuid.UUID
}

// capRank mirrors the Rego capability hierarchy
// (admin > delete > edit > share > view). Higher rank subsumes lower.
func capRank(c model.Capability) int {
	switch c {
	case model.CapAdmin:
		return 50
	case model.CapDelete:
		return 40
	case model.CapEdit:
		return 30
	case model.CapShare:
		return 20
	case model.CapView:
		return 10
	}
	return 0
}

// EffectiveAccess composes the same authoritative cascade as
// GetResourceReaders (direct grants → folder grants/cascade → workspace
// grants/admins) but keeps each principal's capability + the reasons that
// confer it. It does NOT re-implement the Rego rules with new logic — it
// reads the same tables the policy decision reads, in the same cascade
// order — so it cannot drift into a second, contradictory source of truth.
func (s *Service) EffectiveAccess(ctx context.Context, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, workspaceID, folderID *uuid.UUID) (*EffectiveAccess, error) {
	merged := map[string]*EffectivePrincipal{}
	upsert := func(pt model.PrincipalType, pid uuid.UUID, cap model.Capability, reason string) {
		if cap == "" {
			return
		}
		k := string(pt) + ":" + pid.String()
		e := merged[k]
		if e == nil {
			e = &EffectivePrincipal{PrincipalType: pt, PrincipalID: pid}
			merged[k] = e
		}
		if capRank(cap) > capRank(e.Capability) {
			e.Capability = cap
		}
		e.Reasons = append(e.Reasons, string(cap)+" ("+reason+")")
	}

	out := &EffectiveAccess{OrgAdminsHaveAccess: true}

	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		addGrants := func(perms []model.Permission, reason string) {
			for _, p := range perms {
				upsert(p.PrincipalType, p.PrincipalID, p.Capability, reason)
			}
		}
		queryAdmins := func(wsID uuid.UUID) error {
			rows, err := tx.Query(ctx, `
				SELECT user_id FROM workspace_members
				WHERE tenant_id = $1 AND workspace_id = $2 AND role = 'admin'
			`, tenantID, wsID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var uid uuid.UUID
				if rows.Scan(&uid) == nil {
					upsert(model.PrincUser, uid, model.CapAdmin, "workspace admin")
				}
			}
			return rows.Err()
		}

		// Direct grants on the resource itself.
		direct, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, kind, id, s.now())
		if err != nil {
			return err
		}
		addGrants(direct, "direct")

		// Folder context: cascade folder grants + folder_grants ACL +
		// visibility/owner. visFolder is the folder whose visibility governs
		// the workspace-baseline rule.
		var visFolder *uuid.UUID
		if kind == model.ResFolder {
			visFolder = &id
		} else if kind == model.ResDocument && folderID != nil {
			visFolder = folderID
			if f, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, model.ResFolder, *folderID, s.now()); err == nil {
				addGrants(f, "via folder")
			}
		}
		if visFolder != nil {
			fgRows, err := tx.Query(ctx, `
				SELECT grantee_type, grantee_id FROM folder_grants
				WHERE tenant_id = $1 AND folder_id = $2
			`, tenantID, *visFolder)
			if err == nil {
				for fgRows.Next() {
					var gt string
					var gid uuid.UUID
					if fgRows.Scan(&gt, &gid) == nil {
						upsert(model.PrincipalType(gt), gid, model.CapView, "folder grant")
					}
				}
				fgRows.Close()
			}
			var vis string
			var owner *uuid.UUID
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(visibility, 'shared'), owner_id FROM folders
				WHERE tenant_id = $1 AND id = $2
			`, tenantID, *visFolder).Scan(&vis, &owner); err == nil {
				if vis == "private" {
					out.PrivateFolder = true
					if owner != nil {
						out.FolderOwner = owner
						upsert(model.PrincUser, *owner, model.CapAdmin, "folder owner")
					}
				}
			}
		}

		// Workspace context: cascade workspace grants + workspace admins.
		switch {
		case kind == model.ResWorkspace:
			if err := queryAdmins(id); err != nil {
				return err
			}
		case (kind == model.ResDocument || kind == model.ResFolder) && workspaceID != nil:
			if wperms, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, model.ResWorkspace, *workspaceID, s.now()); err == nil {
				addGrants(wperms, "via workspace")
			}
			if err := queryAdmins(*workspaceID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Workspace baseline view (Rego rule 5a): every workspace member can at
	// least view shared content. A private folder overrides it.
	if !out.PrivateFolder {
		switch {
		case kind == model.ResWorkspace:
			out.WorkspaceBaselineView = &id
		case workspaceID != nil:
			out.WorkspaceBaselineView = workspaceID
		}
	}

	out.Principals = make([]EffectivePrincipal, 0, len(merged))
	for _, e := range merged {
		out.Principals = append(out.Principals, *e)
	}
	sort.Slice(out.Principals, func(i, j int) bool {
		if out.Principals[i].Capability != out.Principals[j].Capability {
			return capRank(out.Principals[i].Capability) > capRank(out.Principals[j].Capability)
		}
		return out.Principals[i].PrincipalID.String() < out.Principals[j].PrincipalID.String()
	})
	return out, nil
}
