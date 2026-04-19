package service

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

// ListByResource returns the current active ACL for a resource. Used by the
// permission editor UI.
func (s *Service) ListByResource(ctx context.Context, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID) ([]model.Permission, error) {
	var out []model.Permission
	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		ps, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, kind, id, s.now())
		out = ps
		return err
	})
	return out, err
}

// ListByPrincipal returns the current active grants for a user or group.
func (s *Service) ListByPrincipal(ctx context.Context, tenantID uuid.UUID, kind model.PrincipalType, id uuid.UUID) ([]model.Permission, error) {
	var out []model.Permission
	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		ps, err := s.repos.Permissions.ListByPrincipal(ctx, tx, tenantID, kind, id, s.now())
		out = ps
		return err
	})
	return out, err
}

// ListAsOf is the "time machine" read. Returns permissions that were active
// at the given instant, regardless of current state. Used for compliance
// audits: "who had access to doc X on March 15?"
func (s *Service) ListAsOf(ctx context.Context, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, asOf time.Time) ([]model.Permission, error) {
	var out []model.Permission
	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		ps, err := s.repos.Permissions.ListByResourceAsOf(ctx, tx, tenantID, kind, id, asOf)
		out = ps
		return err
	})
	return out, err
}

// ResourceReaders is what the Search service asks for at index time: the
// principal IDs that can VIEW (or higher) a resource. The result contains:
//   - direct user grants (principal_type=user)
//   - group grants (principal_type=group)
//   - workspace admin user_ids (from workspace_members.role=admin where
//     the workspace matches)
//
// Output is de-duplicated and sorted for stable indexing.
type ResourceReaders struct {
	UserIDs  []uuid.UUID
	GroupIDs []uuid.UUID
}

// GetResourceReaders materializes the readable_by set. NOTE: org admins
// and owners are NOT enumerated here — callers treat them as implicit
// "everyone" via a separate role-based check in the search query.
func (s *Service) GetResourceReaders(ctx context.Context, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, workspaceID *uuid.UUID, folderID *uuid.UUID) (*ResourceReaders, error) {
	seenUsers := map[uuid.UUID]struct{}{}
	seenGroups := map[uuid.UUID]struct{}{}

	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		collect := func(perms []model.Permission) {
			for _, p := range perms {
				if p.Capability == "" {
					continue
				}
				switch p.PrincipalType {
				case model.PrincUser:
					seenUsers[p.PrincipalID] = struct{}{}
				case model.PrincGroup:
					seenGroups[p.PrincipalID] = struct{}{}
				}
			}
		}
		direct, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, kind, id, s.now())
		if err != nil {
			return err
		}
		collect(direct)

		if kind == model.ResDocument && folderID != nil {
			if f, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, model.ResFolder, *folderID, s.now()); err == nil {
				collect(f)
			}
		}
		if (kind == model.ResDocument || kind == model.ResFolder) && workspaceID != nil {
			if w, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, model.ResWorkspace, *workspaceID, s.now()); err == nil {
				collect(w)
			}
			// Workspace admins can always read — add their user IDs.
			rows, err := tx.Query(ctx, `
				SELECT user_id FROM workspace_members
				WHERE tenant_id = $1 AND workspace_id = $2 AND role = 'admin'
			`, tenantID, *workspaceID)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var uid uuid.UUID
				if err := rows.Scan(&uid); err == nil {
					seenUsers[uid] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	users := make([]uuid.UUID, 0, len(seenUsers))
	for u := range seenUsers {
		users = append(users, u)
	}
	groups := make([]uuid.UUID, 0, len(seenGroups))
	for g := range seenGroups {
		groups = append(groups, g)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].String() < users[j].String() })
	sort.Slice(groups, func(i, j int) bool { return groups[i].String() < groups[j].String() })
	return &ResourceReaders{UserIDs: users, GroupIDs: groups}, nil
}
