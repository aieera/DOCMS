// Package service is the policy service's orchestration layer:
// repository + Redis cache + OPA engine + NATS outbox.
//
// Hot path flow for CheckPermission:
//  1. Load resource permissions (Redis → Postgres)
//  2. Load user's group memberships (Redis → Postgres)
//  3. Load user's workspace memberships (Redis → Postgres)
//  4. Feed everything into the pre-compiled Rego query
//  5. Return (allowed, reason)
package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/policy/internal/cache"
	"github.com/aieera/sedoc/services/policy/internal/model"
	"github.com/aieera/sedoc/services/policy/internal/opa"
	"github.com/aieera/sedoc/services/policy/internal/repository"
)

// Service exposes all policy operations. Stateless apart from handles.
type Service struct {
	repos  *repository.Bundle
	cache  *cache.Cache
	engine *opa.Engine
	outbox *database.OutboxRepository
	log    zerolog.Logger
	now    func() time.Time
}

// Config bundles DI.
type Config struct {
	Repos  *repository.Bundle
	Cache  *cache.Cache
	Engine *opa.Engine
	Outbox *database.OutboxRepository
	Logger zerolog.Logger
}

// New wires a Service.
func New(cfg Config) *Service {
	return &Service{
		repos:  cfg.Repos,
		cache:  cfg.Cache,
		engine: cfg.Engine,
		outbox: cfg.Outbox,
		log:    cfg.Logger,
		now:    time.Now,
	}
}

// ---- Cache-aware loaders --------------------------------------------------

// loadResourcePermissions gets the (cached) permission list for a resource,
// plus — for documents — cascading folder and workspace permissions.
// Returns PermissionDoc slice ready for Rego.
func (s *Service) loadResourcePermissions(ctx context.Context, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, folderID, workspaceID string) ([]opa.PermissionDoc, error) {
	direct, err := s.loadPermsForOne(ctx, tenantID, string(kind), id)
	if err != nil {
		return nil, err
	}

	out := direct
	// Cascading loads for documents: folder + workspace.
	if kind == model.ResDocument && folderID != "" {
		if fid, err := uuid.Parse(folderID); err == nil {
			folderPerms, _ := s.loadPermsForOne(ctx, tenantID, string(model.ResFolder), fid)
			out = append(out, folderPerms...)
		}
	}
	if (kind == model.ResDocument || kind == model.ResFolder) && workspaceID != "" {
		if wid, err := uuid.Parse(workspaceID); err == nil {
			wsPerms, _ := s.loadPermsForOne(ctx, tenantID, string(model.ResWorkspace), wid)
			out = append(out, wsPerms...)
		}
	}
	return out, nil
}

// loadPermsForOne is the cache-then-DB primitive. Serializes PermissionDoc
// so the cached representation matches what Rego consumes.
func (s *Service) loadPermsForOne(ctx context.Context, tenantID uuid.UUID, kind string, id uuid.UUID) ([]opa.PermissionDoc, error) {
	key := cache.ResourcePermsKey(tenantID, kind, id)
	var docs []opa.PermissionDoc
	if hit, err := s.cache.GetJSON(ctx, key, &docs); err == nil && hit {
		return docs, nil
	}

	err := database.WithTenant(ctx, s.repos.Pool, tenantID, func(conn *pgxpoolConn) error {
		return withTx(ctx, conn, func(tx pgx.Tx) error {
			perms, err := s.repos.Permissions.ListByResource(ctx, tx, tenantID, model.ResourceType(kind), id, s.now())
			if err != nil {
				return err
			}
			docs = permsToDocs(perms)
			// Fire-and-forget cache write; OK to ignore errors.
			_ = s.cache.SetJSON(ctx, key, docs)
			// Index by group so that group-membership changes can fan out.
			for _, p := range perms {
				if p.PrincipalType == model.PrincGroup {
					_ = s.cache.TrackResourceByGroup(ctx, tenantID, p.PrincipalID, kind, id)
				}
			}
			return nil
		})
	})
	return docs, err
}

// loadUserGroups caches the set of groups a user belongs to.
func (s *Service) loadUserGroups(ctx context.Context, tenantID, userID uuid.UUID) ([]string, error) {
	key := cache.UserGroupsKey(tenantID, userID)
	var out []string
	if hit, err := s.cache.GetJSON(ctx, key, &out); err == nil && hit {
		return out, nil
	}
	err := database.WithTenant(ctx, s.repos.Pool, tenantID, func(conn *pgxpoolConn) error {
		return withTx(ctx, conn, func(tx pgx.Tx) error {
			ids, err := s.repos.Groups.GroupsForUser(ctx, tx, tenantID, userID)
			if err != nil {
				return err
			}
			out = make([]string, 0, len(ids))
			for _, id := range ids {
				out = append(out, id.String())
			}
			_ = s.cache.SetJSON(ctx, key, out)
			return nil
		})
	})
	return out, err
}

// loadUserWorkspaces caches workspace memberships with role.
func (s *Service) loadUserWorkspaces(ctx context.Context, tenantID, userID uuid.UUID) ([]opa.WorkspaceDoc, error) {
	key := cache.UserWorkspacesKey(tenantID, userID)
	var out []opa.WorkspaceDoc
	if hit, err := s.cache.GetJSON(ctx, key, &out); err == nil && hit {
		return out, nil
	}
	err := database.WithTenant(ctx, s.repos.Pool, tenantID, func(conn *pgxpoolConn) error {
		return withTx(ctx, conn, func(tx pgx.Tx) error {
			ms, err := s.repos.Workspaces.MembershipsForUser(ctx, tx, tenantID, userID)
			if err != nil {
				return err
			}
			out = make([]opa.WorkspaceDoc, 0, len(ms))
			for _, m := range ms {
				out = append(out, opa.WorkspaceDoc{
					UserID:      m.UserID.String(),
					WorkspaceID: m.WorkspaceID.String(),
					Role:        m.Role,
				})
			}
			_ = s.cache.SetJSON(ctx, key, out)
			return nil
		})
	})
	return out, err
}

// ---- helpers --------------------------------------------------------------

func permsToDocs(ps []model.Permission) []opa.PermissionDoc {
	out := make([]opa.PermissionDoc, 0, len(ps))
	for _, p := range ps {
		d := opa.PermissionDoc{
			ResourceType:  string(p.ResourceType),
			ResourceID:    p.ResourceID.String(),
			PrincipalType: string(p.PrincipalType),
			PrincipalID:   p.PrincipalID.String(),
			Capability:    string(p.Capability),
		}
		if p.ValidTo != nil {
			d.ValidTo = p.ValidTo.Format(time.RFC3339)
		}
		if p.ExpiresAt != nil {
			d.ExpiresAt = p.ExpiresAt.Format(time.RFC3339)
		}
		out = append(out, d)
	}
	return out
}

// emitAuth publishes an outbox event via the tenant-scoped pool. Caller
// passes an already-open TX so the event lives alongside the business write.
func (s *Service) emit(ctx context.Context, tx pgx.Tx, tenantID, aggregateID uuid.UUID, eventType string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, eventType, "permission", aggregateID, body)
	return s.outbox.Insert(ctx, tx, evt)
}

// ctxWithTenant is a no-op shim retained for readability in call sites.
func ctxWithTenant(ctx context.Context) context.Context { return ctx }

// Used by other files in the package.
var _ = strings.EqualFold
