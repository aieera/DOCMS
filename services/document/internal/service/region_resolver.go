package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/regionenforcer"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// resolveRegionForCreate implements the Blueprint §9.1 resolution chain:
//
//	explicit request → workspace.region_pin → org.default_region_pin → "us-east-1"
//
// It then validates the resolved region against organizations.allowed_regions
// (if set — NULL means no restriction, preserved for pre-Wave-15 tenants).
// On rejection the tx emits dms.residency.violation.v1 so the audit chain
// carries a trail of every attempted breach, not just successful migrations.
//
// The fallback stays "us-east-1" rather than "me-south-1" inside this
// function — the per-tenant default_region_pin column does the real
// policy switch (new tenants default to me-south-1, existing stay on
// us-east-1). The hardcoded fallback only fires if default_region_pin
// somehow ended up empty, which the NOT NULL constraint prevents on
// fresh schemas.
func (s *DocumentService) resolveRegionForCreate(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, userID, workspaceID uuid.UUID,
	requested string,
) (string, error) {
	if requested != "" && !regionenforcer.IsKnown(requested) {
		return "", vdmserr.Validation("region_pin", "unsupported region")
	}

	var (
		orgDefault  string
		allowed     []string
		wsRegion    string
	)
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(default_region_pin, 'us-east-1'), COALESCE(allowed_regions, '{}'::text[])
		FROM organizations WHERE id = $1
	`, tenantID).Scan(&orgDefault, &allowed)
	if err != nil {
		return "", fmt.Errorf("resolve org region config: %w", err)
	}
	if workspaceID != uuid.Nil {
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(region_pin, '') FROM workspaces WHERE tenant_id = $1 AND id = $2`,
			tenantID, workspaceID,
		).Scan(&wsRegion); err != nil && err != pgx.ErrNoRows {
			return "", fmt.Errorf("resolve workspace region: %w", err)
		}
	}

	chosen := firstNonEmpty(requested, wsRegion, orgDefault, "us-east-1")

	if len(allowed) > 0 && !contains(allowed, chosen) {
		// Emit violation event on the same tx so the audit chain sees
		// the attempt even though the document row never gets written.
		_ = s.emitRegionViolation(ctx, tx, tenantID, userID,
			"not_allowed", chosen, workspaceID, requested, "create_document")
		return "", regionViolationErr(chosen, "not in organization's allowed_regions")
	}
	return chosen, nil
}

// emitRegionViolation appends a dms.residency.violation.v1 outbox event.
// Subject matches the audit service's dms.residency.> filter so the
// /admin/audit-log page picks it up alongside migration events. Called
// from (a) the resolver above and (b) any explicit cross-boundary move
// attempt downstream.
func (s *DocumentService) emitRegionViolation(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, userID uuid.UUID,
	reason, attemptedRegion string,
	workspaceID uuid.UUID,
	requested, operation string,
) error {
	evt, err := model.NewOutboxEvent(tenantID,
		"dms.residency.violation.v1", "document", uuid.Nil,
		map[string]any{
			"tenant_id":        tenantID.String(),
			"actor_id":         userID.String(),
			"workspace_id":     workspaceID.String(),
			"requested_region": requested,
			"resolved_region":  attemptedRegion,
			"operation":        operation,
			"reason":           reason,
		})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

func regionViolationErr(region, msg string) error {
	e := vdmserr.Wrap(vdmserr.ErrRegionViolation,
		fmt.Errorf("region %q not allowed: %s", region, msg))
	e.Message = fmt.Sprintf("region %q not allowed: %s", region, msg)
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details["region"] = region
	return e
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
