package service

import (
	"context"
	"fmt"
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
	// GranterCapability is what the caller holds on the resource. It bounds
	// which existing grants this one may supersede, so a share-level granter
	// cannot strip an admin by re-sharing at a lower level. Empty means
	// fully privileged (internal callers).
	GranterCapability model.Capability
}

// Grant gives a principal a capability on a resource, invalidates the
// relevant cache keys, and emits dms.permission.granted.v1.
//
// Idempotent by design — sharing is a user-facing action people repeat, and
// the previous insert-only implementation turned every repeat into an error:
//
//   - the principal already holds this capability → returns the existing
//     grant untouched, no event;
//   - a revoked or expired row occupies the slot → revived in place. The
//     unique constraint spans (tenant, resource, principal, capability) with
//     no time dimension, so re-inserting is impossible: without this, revoking
//     someone and re-sharing them at the same level failed permanently;
//   - the principal holds a different access level → the old level is
//     superseded so grants do not stack (see supersededCapabilities for the
//     guard that stops a lesser granter stripping a higher one).
//
// GranterCapability bounds what may be superseded; leave it empty for
// internal callers that are implicitly fully privileged.
func (s *Service) Grant(ctx context.Context, in GrantInput) (*model.Permission, error) {
	if err := validateGrant(in); err != nil {
		return nil, err
	}
	granterCap := in.GranterCapability
	if granterCap == "" {
		granterCap = model.CapAdmin
	}

	var granted *model.Permission
	var superseded []model.Permission
	// Whether this call actually changed the grant. Tracked explicitly rather
	// than inferred from timestamps: Postgres truncates to microseconds, so a
	// round-tripped granted_at never compares equal to the Go value we sent.
	var changed bool
	err := database.WithTenantTx(ctx, s.repos.Pool, in.TenantID, func(tx pgx.Tx) error {
		existing, err := s.repos.Permissions.ListForPrincipalOnResource(ctx, tx,
			in.TenantID, in.ResourceType, in.ResourceID, in.PrincipalType, in.PrincipalID)
		if err != nil {
			return err
		}

		now := s.now()
		var same *model.Permission
		held := make([]model.Capability, 0, len(existing))
		byCapability := make(map[model.Capability]model.Permission, len(existing))
		for _, p := range existing {
			if p.Capability == in.Capability {
				same = &p
				continue
			}
			if !isActive(p, now) {
				continue
			}
			held = append(held, p.Capability)
			byCapability[p.Capability] = p
		}

		switch {
		case same != nil && isActive(*same, now):
			// Already holds exactly this — nothing to write, nothing to emit.
			granted = same
		case same != nil:
			// Revoked or expired tombstone: revive it in place.
			revived, err := s.repos.Permissions.Reactivate(ctx, tx,
				in.TenantID, same.ID, in.GrantedBy, now, in.ExpiresAt)
			if err != nil {
				return err
			}
			granted = revived
			changed = true
		default:
			id, err := uuid.NewV7()
			if err != nil {
				return err
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
				GrantedAt:     now,
				ValidFrom:     now,
				ValidTo:       in.ValidTo,
				ExpiresAt:     in.ExpiresAt,
			}
			if err := s.repos.Permissions.Insert(ctx, tx, p); err != nil {
				return err
			}
			granted = p
			changed = true
		}

		// Retire the levels this grant replaces so a principal carries one
		// access level, not an archaeology of every level they were ever given.
		for _, cap := range supersededCapabilities(held, in.Capability, granterCap) {
			old := byCapability[cap]
			revoked, err := s.repos.Permissions.Revoke(ctx, tx, in.TenantID, old.ID, now)
			if err != nil {
				return err
			}
			superseded = append(superseded, *revoked)
			if err := s.emitRevoked(ctx, tx, revoked, in.GrantedBy); err != nil {
				return err
			}
		}

		if !changed {
			// Pre-existing active grant — no state changed, so no event.
			return nil
		}
		if err := s.emit(ctx, tx, in.TenantID, granted.ID, "dms.permission.granted.v1", map[string]any{
			"permission_id":  granted.ID.String(),
			"resource_type":  string(granted.ResourceType),
			"resource_id":    granted.ResourceID.String(),
			"principal_type": string(granted.PrincipalType),
			"principal_id":   granted.PrincipalID.String(),
			"capability":     string(granted.Capability),
			"granted_by":     granted.GrantedBy.String(),
		}); err != nil {
			return err
		}
		return s.notifyGrantee(ctx, tx, granted)
	})
	if err != nil {
		return nil, err
	}

	s.invalidateForPermission(ctx, granted)
	for i := range superseded {
		s.invalidateForPermission(ctx, &superseded[i])
	}
	return granted, nil
}

// notifyGrantee raises an in-app notification for the person just given
// access.
//
// dms.permission.granted.v1 is an audit/cache-invalidation event — nothing
// consumes it for the inbox, so being shared a workspace produced no signal
// whatsoever: the workspace simply appeared in your sidebar one day with no
// indication of who added you or when. The notification consumer binds
// `dms.notify.>` and reads `data` as a DeliveryPayload, so the payload shape
// below is fixed by that contract (tenant_id + user_ids are mandatory; an
// event missing them is Term'd and silently dropped).
//
// Group grants are skipped: expanding a group to its members belongs to the
// notification service's role_resolver, and fanning out here would need the
// policy service to own group membership resolution too.
func (s *Service) notifyGrantee(ctx context.Context, tx pgx.Tx, p *model.Permission) error {
	if p.PrincipalType != model.PrincUser {
		return nil
	}
	// Granting yourself access is a no-op worth no notification.
	if p.PrincipalID == p.GrantedBy {
		return nil
	}
	noun := string(p.ResourceType)
	payload := map[string]any{
		"tenant_id":     p.TenantID.String(),
		"user_ids":      []string{p.PrincipalID.String()},
		"type":          noun + ".shared",
		"title":         shareTitleFor(p.ResourceType),
		"body":          shareBodyFor(p.ResourceType, p.Capability),
		"resource_type": noun,
		"resource_id":   p.ResourceID.String(),
	}
	return s.emit(ctx, tx, p.TenantID, p.ID, "dms.notify."+noun+"_shared.v1", payload)
}

func shareTitleFor(rt model.ResourceType) string {
	switch rt {
	case model.ResWorkspace:
		return "A workspace was shared with you"
	case model.ResFolder:
		return "A folder was shared with you"
	default:
		return "A document was shared with you"
	}
}

// The capability is the useful part — "you can view" vs "you can edit"
// changes what the person does next.
func shareBodyFor(rt model.ResourceType, c model.Capability) string {
	verb := map[model.Capability]string{
		model.CapView:   "view",
		model.CapEdit:   "edit",
		model.CapShare:  "share",
		model.CapDelete: "delete",
		model.CapAdmin:  "administer",
	}[c]
	if verb == "" {
		verb = string(c)
	}
	return fmt.Sprintf("You now have permission to %s this %s.", verb, string(rt))
}

// isActive reports whether a grant is in force at t — not yet started,
// revoked (valid_to) and expired (expires_at) rows all count as inactive.
func isActive(p model.Permission, t time.Time) bool {
	if p.ValidFrom.After(t) {
		return false
	}
	if p.ValidTo != nil && !p.ValidTo.After(t) {
		return false
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(t) {
		return false
	}
	return true
}

func (s *Service) emitRevoked(ctx context.Context, tx pgx.Tx, p *model.Permission, actorID uuid.UUID) error {
	return s.emit(ctx, tx, p.TenantID, p.ID, "dms.permission.revoked.v1", map[string]any{
		"permission_id":  p.ID.String(),
		"resource_type":  string(p.ResourceType),
		"resource_id":    p.ResourceID.String(),
		"principal_type": string(p.PrincipalType),
		"principal_id":   p.PrincipalID.String(),
		"capability":     string(p.Capability),
		"revoked_by":     actorID.String(),
	})
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
			"permission_id":  id.String(),
			"resource_type":  string(p.ResourceType),
			"resource_id":    p.ResourceID.String(),
			"principal_type": string(p.PrincipalType),
			"principal_id":   p.PrincipalID.String(),
			"capability":     string(p.Capability),
			"revoked_by":     actorID.String(),
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
