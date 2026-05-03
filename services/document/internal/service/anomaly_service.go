// Anomaly service surface (ADR 0058). Read + review only — the "run"
// trigger lives on the intelligence service (POST /api/v1/intelligence/
// anomaly/run).
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func (s *DocumentService) ListAnomalyReports(ctx context.Context, opts repository.ListReportsOpts) ([]repository.AnomalyReport, int64, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, 0, err
	}
	var (
		rows  []repository.AnomalyReport
		total int64
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		rows, total, lerr = s.repos.Anomaly.ListReports(ctx, tx, tenantID, opts)
		return lerr
	})
	return rows, total, err
}

// GetAnomalyReport bundles the report + its findings.
func (s *DocumentService) GetAnomalyReport(ctx context.Context, id uuid.UUID) (*repository.AnomalyReport, []repository.AnomalyFinding, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	var (
		report   *repository.AnomalyReport
		findings []repository.AnomalyFinding
	)
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rep, gErr := s.repos.Anomaly.GetReport(ctx, tx, tenantID, id)
		if gErr != nil {
			return gErr
		}
		report = rep
		f, lErr := s.repos.Anomaly.ListFindingsForReport(ctx, tx, tenantID, id)
		if lErr != nil {
			return lErr
		}
		findings = f
		return nil
	})
	return report, findings, err
}

// ResolveAnomalyFinding flips a finding's status. Caller must be admin
// (handler enforces). Emits dms.anomaly.reviewed.v1.
func (s *DocumentService) ResolveAnomalyFinding(ctx context.Context, findingID uuid.UUID, newStatus, note string) (*repository.AnomalyFinding, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	switch newStatus {
	case "open", "acknowledged", "resolved", "false_positive":
	default:
		return nil, vdmserr.Validation("status", "must be open|acknowledged|resolved|false_positive")
	}
	var out *repository.AnomalyFinding
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		updated, uErr := s.repos.Anomaly.UpdateFindingStatus(ctx, tx, tenantID, findingID, userID, newStatus, note)
		if uErr != nil {
			return uErr
		}
		out = updated
		evt, oErr := model.NewOutboxEvent(tenantID, "dms.anomaly.reviewed.v1", "anomaly_finding", findingID,
			anomalyReviewedPayload{
				FindingID:   findingID.String(),
				TenantID:    tenantID.String(),
				DocumentID:  updated.DocumentID.String(),
				ReportID:    updated.ReportID.String(),
				AnomalyType: updated.AnomalyType,
				NewStatus:   newStatus,
				ReviewedBy:  userID.String(),
				ReviewedAt:  time.Now().UTC().Format(time.RFC3339Nano),
				Note:        note,
			})
		if oErr != nil {
			return oErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

func (s *DocumentService) GetAnomalyConfig(ctx context.Context) (*repository.AnomalyConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *repository.AnomalyConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, gErr := s.repos.Anomaly.GetConfig(ctx, tx, tenantID)
		if gErr != nil && !isNotFound(gErr) {
			return gErr
		}
		out = c
		return nil
	})
	return out, err
}

func (s *DocumentService) UpsertAnomalyConfig(ctx context.Context, p repository.AnomalyConfigPatch) (*repository.AnomalyConfig, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateAnomalyPatch(p); err != nil {
		return nil, err
	}
	var out *repository.AnomalyConfig
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		c, uErr := s.repos.Anomaly.UpsertConfig(ctx, tx, tenantID, p)
		if uErr != nil {
			return uErr
		}
		out = c
		return nil
	})
	return out, err
}

func validateAnomalyPatch(p repository.AnomalyConfigPatch) error {
	if p.ZScoreThreshold != nil && *p.ZScoreThreshold <= 0 {
		return vdmserr.Validation("z_score_threshold", "must be > 0")
	}
	if p.ContentDistanceThreshold != nil && (*p.ContentDistanceThreshold < 0 || *p.ContentDistanceThreshold > 2) {
		return vdmserr.Validation("content_distance_threshold", "must be in [0.0, 2.0]")
	}
	if p.MinDocumentsForAnalysis != nil && *p.MinDocumentsForAnalysis < 5 {
		return vdmserr.Validation("min_documents_for_analysis", "must be >= 5")
	}
	return nil
}

type anomalyReviewedPayload struct {
	FindingID   string `json:"finding_id"`
	TenantID    string `json:"tenant_id"`
	DocumentID  string `json:"document_id"`
	ReportID    string `json:"report_id"`
	AnomalyType string `json:"anomaly_type"`
	NewStatus   string `json:"new_status"`
	ReviewedBy  string `json:"reviewed_by"`
	ReviewedAt  string `json:"reviewed_at"`
	Note        string `json:"note,omitempty"`
}
