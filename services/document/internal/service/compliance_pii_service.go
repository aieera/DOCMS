// Compliance PII/PHI service surface — wraps the new compliance repo
// with permission checks and outbox writes. Distinct from the existing
// compliance package (legal holds).
package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

// GetComplianceForDocument loads the per-document summary + findings.
// View permission required (compliance state is metadata about the doc).
func (s *DocumentService) GetComplianceForDocument(ctx context.Context, documentID uuid.UUID) (*repository.ComplianceSummary, []repository.ComplianceFinding, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.requirePermission(ctx, userID, "view", "document", documentID, nil); err != nil {
		return nil, nil, err
	}
	var (
		summary  *repository.ComplianceSummary
		findings []repository.ComplianceFinding
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		sum, sErr := s.repos.Compliance.GetSummary(ctx, tx, tenantID, documentID)
		if sErr != nil && !isNotFound(sErr) {
			return sErr
		}
		summary = sum
		f, fErr := s.repos.Compliance.ListFindings(ctx, tx, tenantID, documentID)
		if fErr != nil {
			return fErr
		}
		findings = f
		return nil
	})
	return summary, findings, err
}

// ReviewComplianceFinding flips a finding's remediation_status.
// edit permission on the document required for any state change.
func (s *DocumentService) ReviewComplianceFinding(ctx context.Context, documentID, findingID uuid.UUID, newStatus, note string) (*repository.ComplianceFinding, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	switch newStatus {
	case "acknowledged", "remediated", "false_positive", "open":
	default:
		return nil, vdmserr.Validation("status", "must be acknowledged|remediated|false_positive|open")
	}
	if err := s.requirePermission(ctx, userID, "edit", "document", documentID, nil); err != nil {
		return nil, err
	}
	var out *repository.ComplianceFinding
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, gErr := s.repos.Compliance.GetFinding(ctx, tx, tenantID, findingID)
		if gErr != nil {
			return gErr
		}
		if cur.DocumentID != documentID {
			return vdmserr.Validation("finding_id", "belongs to a different document")
		}
		updated, uErr := s.repos.Compliance.UpdateFindingStatus(ctx, tx, tenantID, findingID, userID, newStatus, note)
		if uErr != nil {
			return uErr
		}
		out = updated
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.compliance.reviewed.v1", "document", documentID,
			complianceReviewedPayload{
				DocumentID:        documentID.String(),
				TenantID:          tenantID.String(),
				FindingID:         findingID.String(),
				EntityType:        updated.EntityType,
				EntityCategory:    updated.EntityCategory,
				NewStatus:         newStatus,
				ReviewedBy:        userID.String(),
				ReviewedAt:        time.Now().UTC().Format(time.RFC3339Nano),
				Note:              note,
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

// ListPendingComplianceFindings — admin queue.
func (s *DocumentService) ListPendingComplianceFindings(ctx context.Context, opts repository.ListFindingsOpts) ([]repository.ComplianceFinding, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	var (
		rows  []repository.ComplianceFinding
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.Compliance.ListPendingFindings(ctx, tx, tenantID, opts)
		return lerr
	})
	return rows, total, err
}

func (s *DocumentService) ComplianceDashboard(ctx context.Context) (*repository.ComplianceDashboard, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.ComplianceDashboard
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		d, dErr := s.repos.Compliance.Dashboard(ctx, tx, tenantID)
		if dErr != nil {
			return dErr
		}
		out = d
		return nil
	})
	return out, err
}

// GetComplianceConfig — admin only at the handler layer.
func (s *DocumentService) GetComplianceConfig(ctx context.Context) (*repository.ComplianceConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.ComplianceConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.Compliance.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertComplianceConfig(ctx context.Context, p repository.ComplianceConfigPatch) (*repository.ComplianceConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateComplianceConfigPatch(p); err != nil {
		return nil, err
	}
	var out *repository.ComplianceConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.Compliance.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

// RescanCompliance emits the trigger event so intelligence picks the
// document up again. Admin permission required because this can spike
// worker load.
func (s *DocumentService) RescanCompliance(ctx context.Context, documentID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := s.requirePermission(ctx, userID, "admin", "document", documentID, nil); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Resolve current version_id so the worker doesn't have to re-derive.
		var versionID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM versions WHERE tenant_id = $1 AND document_id = $2
			 ORDER BY version_number DESC LIMIT 1`,
			tenantID, documentID,
		).Scan(&versionID); err != nil {
			return err
		}
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.compliance.rescan_requested.v1", "document", documentID,
			complianceRescanPayload{
				DocumentID:   documentID.String(),
				TenantID:     tenantID.String(),
				VersionID:    versionID.String(),
				RequestedBy:  userID.String(),
				RequestedAt:  time.Now().UTC().Format(time.RFC3339Nano),
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// ---- validators ---------------------------------------------------------

func validateComplianceConfigPatch(p repository.ComplianceConfigPatch) error {
	if p.NotifyRoles != nil && len(*p.NotifyRoles) == 0 {
		return vdmserr.Validation("notify_roles", "must include at least one role")
	}
	if p.PIIEntityRiskOverrides != nil {
		var m map[string]string
		if err := json.Unmarshal(*p.PIIEntityRiskOverrides, &m); err != nil {
			return vdmserr.Validation("pii_entity_risk_overrides", "must be a JSON object {entity_type: risk_level}")
		}
		valid := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
		for k, v := range m {
			if !valid[v] {
				return vdmserr.Validation("pii_entity_risk_overrides",
					"risk for "+k+" must be critical|high|medium|low")
			}
		}
	}
	if p.CustomPatterns != nil {
		var arr []map[string]any
		if err := json.Unmarshal(*p.CustomPatterns, &arr); err != nil {
			return vdmserr.Validation("custom_patterns", "must be a JSON array")
		}
		if len(arr) > 32 {
			return vdmserr.Validation("custom_patterns", "max 32 patterns")
		}
	}
	return nil
}

// ---- outbox payloads ----------------------------------------------------

type complianceReviewedPayload struct {
	DocumentID     string `json:"document_id"`
	TenantID       string `json:"tenant_id"`
	FindingID      string `json:"finding_id"`
	EntityType     string `json:"entity_type"`
	EntityCategory string `json:"entity_category"`
	NewStatus      string `json:"new_status"`
	ReviewedBy     string `json:"reviewed_by"`
	ReviewedAt     string `json:"reviewed_at"`
	Note           string `json:"note,omitempty"`
}

type complianceRescanPayload struct {
	DocumentID  string `json:"document_id"`
	TenantID    string `json:"tenant_id"`
	VersionID   string `json:"version_id"`
	RequestedBy string `json:"requested_by"`
	RequestedAt string `json:"requested_at"`
}
