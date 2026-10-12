package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/policy/internal/model"
	"github.com/aieera/sedoc/services/policy/internal/repository"
)

// ResourceContext is the ABAC context for one (caller, resource) pair,
// derived from the database rather than from the request (M-11, CB-3).
//
// The gRPC path receives this context from the document service, which
// loads the document first. The HTTP surface (the share dialog, and the
// collaboration server's room authorization) has no trusted caller to do
// that, so it resolves the same facts here: folder and workspace for the
// inheritance rules, lifecycle state for the disposed-document deny, the
// caller's status, and the classification/clearance gate. A request body
// never contributes to it.
type ResourceContext struct {
	// Found is false when the resource does not exist or is trashed; the
	// caller must then deny.
	Found bool

	base map[string]string
	doc  *repository.DocumentFacts
	gate repository.ClassificationGate
	// clearance is the caller's clearance, for the gate.
	clearance string
}

// For returns the context map for one action: the base facts plus, for a
// document under an enabled classification gate, the clearance the action
// requires.
func (rc *ResourceContext) For(action string) map[string]string {
	out := make(map[string]string, len(rc.base)+6)
	for k, v := range rc.base {
		out[k] = v
	}
	if rc.doc != nil && rc.gate.Enabled {
		eff := effectiveClassification(rc.gate.PHIRequiresRestricted, rc.doc.SecurityClassification, rc.doc.HasPHI, rc.doc.HasPII)
		out["classification_gate"] = "on"
		out["security_classification"] = eff
		out["has_phi"] = boolStr(rc.doc.HasPHI)
		out["has_pii"] = boolStr(rc.doc.HasPII)
		out["user_clearance"] = rc.clearance
		out["required_clearance"] = requiredClearance(rc.gate.Rules, eff, rc.doc.HasPHI, action)
	}
	return out
}

// ResolveResourceContext loads the server-side context for subject userID
// (tenant role `role`, from the authenticated session) acting on
// (kind, id), in one tenant transaction.
func (s *Service) ResolveResourceContext(ctx context.Context, tenantID, userID uuid.UUID, role string, kind model.ResourceType, id uuid.UUID) (*ResourceContext, error) {
	rc := &ResourceContext{base: map[string]string{"user_role": role}}
	err := database.WithTenantTx(ctx, s.repos.Pool, tenantID, func(tx pgx.Tx) error {
		status, clearance, err := s.repos.Context.UserFacts(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if status != "" {
			rc.base["user_status"] = status
		}
		rc.clearance = clearance

		switch kind {
		case model.ResDocument:
			doc, err := s.repos.Context.DocumentFacts(ctx, tx, tenantID, id)
			if err != nil || doc == nil {
				return err
			}
			rc.Found = true
			rc.doc = doc
			rc.base["workspace_id"] = doc.WorkspaceID.String()
			rc.base["folder_id"] = doc.FolderID.String()
			rc.base["lifecycle_state"] = doc.LifecycleState
			rc.gate, err = s.repos.Context.ClassificationGate(ctx, tx, tenantID)
			return err
		case model.ResFolder:
			ws, ok, err := s.repos.Context.FolderWorkspace(ctx, tx, tenantID, id)
			if err != nil || !ok {
				return err
			}
			rc.Found = true
			rc.base["workspace_id"] = ws.String()
		case model.ResWorkspace:
			// Rule 5 / 5a key off the resource id itself.
			rc.Found = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rc, nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// ---- classification gate ----------------------------------------------
//
// Same resolution as services/document/internal/model/classification.go
// (EffectiveClassification / RequiredClearance), so the HTTP path and the
// document service's gRPC path compute the same required clearance. Ranks
// mirror clearance_levels in opa/policy.rego.

func classificationRank(level string) int {
	switch level {
	case "internal":
		return 1
	case "confidential":
		return 2
	case "restricted":
		return 3
	default: // "", "unclassified", unknown
		return 0
	}
}

// effectiveClassification applies the PHI/PII floors to the stored level.
func effectiveClassification(phiRequiresRestricted bool, class string, hasPHI, hasPII bool) string {
	eff := class
	if hasPHI && phiRequiresRestricted && classificationRank("restricted") > classificationRank(eff) {
		eff = "restricted"
	}
	if hasPII && classificationRank("internal") > classificationRank(eff) {
		eff = "internal"
	}
	return eff
}

// requiredClearance is the strictest matching tenant rule, falling back to
// the identity ladder (a level-X document needs level-X clearance).
func requiredClearance(rules []repository.ClassificationRule, effClass string, hasPHI bool, action string) string {
	req := ""
	if classificationRank(effClass) > 0 {
		req = effClass
	}
	for _, r := range rules {
		if !(r.Action == "*" || r.Action == action || (r.Action == "download" && action == "view")) {
			continue
		}
		applies := classificationRank(effClass) >= classificationRank(r.MinClassification)
		if r.AppliesToPHI && hasPHI {
			applies = true
		}
		if applies && classificationRank(r.RequiredClearance) > classificationRank(req) {
			req = r.RequiredClearance
		}
	}
	return req
}
