package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/policy/internal/model"
)

// GrantInput mirrors the proto request.
type GrantInput struct {
	TenantID      uuid.UUID
	GrantedBy     uuid.UUID
	ResourceType  model.ResourceType
	ResourceID    uuid.UUID
	PrincipalType model.PrincipalType
	PrincipalID   uuid.UUID
	Capability    model.Capability
	ValidTo       *time.Time
	ExpiresAt     *time.Time
}

// Grant writes a new permission row inside the caller's tenant, invalidates
// the relevant cache keys, and emits dms.permission.granted.v1.
func (s *Service) Grant(ctx context.Context, in GrantInput) (*model.Permission, error) {
	if err := validateGrant(in); err != nil {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	p := &model.Permission{
		TenantID:      in.TenantID,
		ID:            id,
		ResourceType:  in.ResourceType,
		ResourceID:    in.ResourceID,
		PrincipalType: in.PrincipalType,
		PrincipalID:   in.PrincipalID,
		Capability:    in.Capability,
		Effect:        model.EffectAllow,
		GrantedBy:     in.GrantedBy,
		GrantedAt:     s.now(),
		ValidFrom:     s.now(),
		ValidTo:       in.ValidTo,
		ExpiresAt:     in.ExpiresAt,
	}

	err = database.WithTenantTx(ctx, s.repos.Pool, in.TenantID, func(tx pgx.Tx) error {
		if err := s.repos.Permissions.Insert(ctx, tx, p); err != nil {
			return err
		}
		return s.emit(ctx, tx, in.TenantID, p.ID, "dms.permission.granted.v1", map[string]any{
			"permission_id":   p.ID.String(),
			"resource_type":   string(p.ResourceType),
			"resource_id":     p.ResourceID.String(),
			"principal_type":  string(p.PrincipalType),
			"principal_id":    p.PrincipalID.String(),
			"capability":      string(p.Capability),
			"granted_by":      p.GrantedBy.String(),
		})
	})
	if err != nil {
		return nil, err
	}

	s.invalidateForPermission(ctx, p)
	return p, nil
}

// Revoke sets valid_to = now(), invalidates cache, emits the event.
func (s *Service) Revoke(ctx context.Context, tenantID, id, actorID uuid.UUID) error {
	if tenantID == uuid.Nil || id == uuid.Nil {
		return vdmserr.Validation("id", "required")
	}
	var revoked *model.Permission
	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		p, err := s.repos.Permissions.Revoke(ctx, tx, tenantID, id, s.now())
		if err != nil {
			return err
		}
		revoked = p
		return s.emit(ctx, tx, tenantID, id, "dms.permission.revoked.v1", map[string]any{
			"permission_id":   id.String(),
			"resource_type":   string(p.ResourceType),
			"resource_id":     p.ResourceID.String(),
			"principal_type":  string(p.PrincipalType),
			"principal_id":    p.PrincipalID.String(),
			"capability":      string(p.Capability),
			"revoked_by":      actorID.String(),
		})
	})
	if err != nil {
		return err
	}
	s.invalidateForPermission(ctx, revoked)
	return nil
}

// invalidateForPermission drops the affected cache keys. For group grants
// we additionally fan out via the perm_by_group set — changing a group's
// ACL should affect every cached resource mentioning that group.
func (s *Service) invalidateForPermission(ctx context.Context, p *model.Permission) {
	if p == nil {
		return
	}
	_ = s.cache.InvalidateResource(ctx, p.TenantID, string(p.ResourceType), p.ResourceID)

	if p.PrincipalType == model.PrincGroup {
		_, _ = s.cache.InvalidateResourcesForGroup(ctx, p.TenantID, p.PrincipalID)
	}
	// User-scoped permissions only affect one resource; the resource cache
	// drop above is sufficient.
}

// ---- validation -----------------------------------------------------------

func validateGrant(in GrantInput) error {
	if in.TenantID == uuid.Nil {
		return vdmserr.Validation("tenant_id", "required")
	}
	if _, ok := validResourceTypes[string(in.ResourceType)]; !ok {
		return vdmserr.Validation("resource_type", "unsupported")
	}
	if in.ResourceID == uuid.Nil {
		return vdmserr.Validation("resource_id", "required")
	}
	if _, ok := validSubjectTypes[string(in.PrincipalType)]; !ok {
		return vdmserr.Validation("principal_type", "unsupported")
	}
	if in.PrincipalID == uuid.Nil {
		return vdmserr.Validation("principal_id", "required")
	}
	switch in.Capability {
	case model.CapView, model.CapShare, model.CapEdit, model.CapDelete, model.CapAdmin:
	default:
		return vdmserr.Validation("capability", "unsupported")
	}
	return nil
}
