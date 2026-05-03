// Repository for ADR 0058 — anomaly reports + findings + per-tenant
// config. The "run" trigger lives on the Python side (intelligence
// service POST /anomaly/run); this Go repo only owns the read /
// review surface.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// ---- types --------------------------------------------------------------

type AnomalyReport struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	WorkspaceID     *uuid.UUID
	AnalysisType    string
	Status          string
	TotalDocuments  int32
	AnomaliesFound  int32
	Summary         json.RawMessage
	ErrorMessage    string
	TriggeredBy     string
	RequestedBy     *uuid.UUID
	CompletedAt     *time.Time
	CreatedAt       time.Time
}

type AnomalyFinding struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	ReportID        uuid.UUID
	DocumentID      uuid.UUID
	AnomalyType     string
	Severity        string // 'high'|'medium'|'low'
	Description     string
	Evidence        json.RawMessage
	ZScore          *float32
	SimilarityScore *float32
	Status          string // 'open'|'acknowledged'|'resolved'|'false_positive'
	ResolvedBy      *uuid.UUID
	ResolvedAt      *time.Time
	ResolutionNote  string
	CreatedAt       time.Time
}

type AnomalyConfig struct {
	TenantID                 uuid.UUID
	Enabled                  bool
	ScheduleCron             string
	ZScoreThreshold          float32
	ContentDistanceThreshold float32
	MinDocumentsForAnalysis  int32
	AnalyzeMetadata          bool
	AnalyzeContent           bool
	AnalyzeBehavioral        bool
}

type AnomalyConfigPatch struct {
	Enabled                  *bool
	ScheduleCron             *string
	ZScoreThreshold          *float32
	ContentDistanceThreshold *float32
	MinDocumentsForAnalysis  *int32
	AnalyzeMetadata          *bool
	AnalyzeContent           *bool
	AnalyzeBehavioral        *bool
}

type ListReportsOpts struct {
	Status      string // empty = all
	WorkspaceID *uuid.UUID
	Limit       int32
	Offset      int32
}

// ---- interface ----------------------------------------------------------

type AnomalyRepository interface {
	ListReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListReportsOpts) ([]AnomalyReport, int64, error)
	GetReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*AnomalyReport, error)
	ListFindingsForReport(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) ([]AnomalyFinding, error)
	GetFinding(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*AnomalyFinding, error)
	UpdateFindingStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus, note string) (*AnomalyFinding, error)

	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*AnomalyConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p AnomalyConfigPatch) (*AnomalyConfig, error)
}

type anomalyRepo struct{}

func NewAnomalyRepo() AnomalyRepository { return &anomalyRepo{} }

// ---- reports ------------------------------------------------------------

const selectAnomalyReportSQL = `
SELECT id, tenant_id, workspace_id, analysis_type, status,
       total_documents, anomalies_found, summary,
       COALESCE(error_message, ''), triggered_by,
       requested_by, completed_at, created_at
  FROM anomaly_reports
`

func (r *anomalyRepo) ListReports(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListReportsOpts) ([]AnomalyReport, int64, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	status := opts.Status
	var workspaceID *uuid.UUID = opts.WorkspaceID

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM anomaly_reports
		 WHERE tenant_id = $1
		   AND ($2 = '' OR status = $2)
		   AND ($3::uuid IS NULL OR workspace_id = $3)`,
		tenantID, status, workspaceID,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}
	rows, err := tx.Query(ctx, selectAnomalyReportSQL+`
		WHERE tenant_id = $1
		  AND ($2 = '' OR status = $2)
		  AND ($3::uuid IS NULL OR workspace_id = $3)
		ORDER BY created_at DESC
		LIMIT $4 OFFSET $5`,
		tenantID, status, workspaceID, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]AnomalyReport, 0, opts.Limit)
	for rows.Next() {
		rep, err := scanAnomalyReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *rep)
	}
	return out, total, mapPgError(rows.Err())
}

func (r *anomalyRepo) GetReport(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*AnomalyReport, error) {
	row := tx.QueryRow(ctx, selectAnomalyReportSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanAnomalyReport(row)
}

// ---- findings -----------------------------------------------------------

const selectAnomalyFindingSQL = `
SELECT id, tenant_id, report_id, document_id, anomaly_type,
       severity, description, evidence, z_score, similarity_score,
       status, resolved_by, resolved_at, COALESCE(resolution_note, ''),
       created_at
  FROM anomaly_findings
`

func (r *anomalyRepo) ListFindingsForReport(ctx context.Context, tx pgx.Tx, tenantID, reportID uuid.UUID) ([]AnomalyFinding, error) {
	rows, err := tx.Query(ctx, selectAnomalyFindingSQL+`
		WHERE tenant_id = $1 AND report_id = $2
		ORDER BY
		  CASE severity WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END,
		  created_at DESC
		LIMIT 1000`,
		tenantID, reportID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]AnomalyFinding, 0, 16)
	for rows.Next() {
		f, err := scanAnomalyFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, mapPgError(rows.Err())
}

func (r *anomalyRepo) GetFinding(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*AnomalyFinding, error) {
	row := tx.QueryRow(ctx, selectAnomalyFindingSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanAnomalyFinding(row)
}

func (r *anomalyRepo) UpdateFindingStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus, note string) (*AnomalyFinding, error) {
	switch newStatus {
	case "open", "acknowledged", "resolved", "false_positive":
	default:
		return nil, vdmserr.Validation("status", "invalid")
	}
	row := tx.QueryRow(ctx, `
		UPDATE anomaly_findings
		   SET status          = $4,
		       resolved_by     = CASE WHEN $4 = 'open' THEN NULL ELSE $3 END,
		       resolved_at     = CASE WHEN $4 = 'open' THEN NULL ELSE now() END,
		       resolution_note = NULLIF($5, '')
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, tenant_id, report_id, document_id, anomaly_type,
		           severity, description, evidence, z_score, similarity_score,
		           status, resolved_by, resolved_at, COALESCE(resolution_note, ''),
		           created_at`,
		tenantID, id, reviewer, newStatus, note,
	)
	f, err := scanAnomalyFinding(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vdmserr.ErrNotFound) {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// ---- config -------------------------------------------------------------

const selectAnomalyConfigSQL = `
SELECT tenant_id, enabled, schedule_cron, z_score_threshold,
       content_distance_threshold, min_documents_for_analysis,
       analyze_metadata, analyze_content, analyze_behavioral
  FROM anomaly_config
`

func (r *anomalyRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*AnomalyConfig, error) {
	row := tx.QueryRow(ctx, selectAnomalyConfigSQL+` WHERE tenant_id = $1`, tenantID)
	return scanAnomalyConfig(row)
}

func (r *anomalyRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p AnomalyConfigPatch) (*AnomalyConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := AnomalyConfig{
		TenantID:                 tenantID,
		Enabled:                  true,
		ScheduleCron:             "0 2 * * 0",
		ZScoreThreshold:          2.5,
		ContentDistanceThreshold: 0.7,
		MinDocumentsForAnalysis:  20,
		AnalyzeMetadata:          true,
		AnalyzeContent:           true,
		AnalyzeBehavioral:        true,
	}
	if cur != nil {
		merged = *cur
	}
	if p.Enabled != nil {
		merged.Enabled = *p.Enabled
	}
	if p.ScheduleCron != nil {
		merged.ScheduleCron = *p.ScheduleCron
	}
	if p.ZScoreThreshold != nil {
		merged.ZScoreThreshold = *p.ZScoreThreshold
	}
	if p.ContentDistanceThreshold != nil {
		merged.ContentDistanceThreshold = *p.ContentDistanceThreshold
	}
	if p.MinDocumentsForAnalysis != nil {
		merged.MinDocumentsForAnalysis = *p.MinDocumentsForAnalysis
	}
	if p.AnalyzeMetadata != nil {
		merged.AnalyzeMetadata = *p.AnalyzeMetadata
	}
	if p.AnalyzeContent != nil {
		merged.AnalyzeContent = *p.AnalyzeContent
	}
	if p.AnalyzeBehavioral != nil {
		merged.AnalyzeBehavioral = *p.AnalyzeBehavioral
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO anomaly_config
		    (tenant_id, enabled, schedule_cron, z_score_threshold,
		     content_distance_threshold, min_documents_for_analysis,
		     analyze_metadata, analyze_content, analyze_behavioral)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled                    = EXCLUDED.enabled,
		       schedule_cron              = EXCLUDED.schedule_cron,
		       z_score_threshold          = EXCLUDED.z_score_threshold,
		       content_distance_threshold = EXCLUDED.content_distance_threshold,
		       min_documents_for_analysis = EXCLUDED.min_documents_for_analysis,
		       analyze_metadata           = EXCLUDED.analyze_metadata,
		       analyze_content            = EXCLUDED.analyze_content,
		       analyze_behavioral         = EXCLUDED.analyze_behavioral,
		       updated_at                 = now()`,
		tenantID, merged.Enabled, merged.ScheduleCron, merged.ZScoreThreshold,
		merged.ContentDistanceThreshold, merged.MinDocumentsForAnalysis,
		merged.AnalyzeMetadata, merged.AnalyzeContent, merged.AnalyzeBehavioral,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

// ---- scanners -----------------------------------------------------------

func scanAnomalyReport(s rowScanner) (*AnomalyReport, error) {
	var (
		out         AnomalyReport
		workspaceID *uuid.UUID
		summary     []byte
		requestedBy *uuid.UUID
		completedAt *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &workspaceID, &out.AnalysisType, &out.Status,
		&out.TotalDocuments, &out.AnomaliesFound, &summary,
		&out.ErrorMessage, &out.TriggeredBy,
		&requestedBy, &completedAt, &out.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.WorkspaceID = workspaceID
	out.Summary = json.RawMessage(summary)
	out.RequestedBy = requestedBy
	out.CompletedAt = completedAt
	return &out, nil
}

func scanAnomalyFinding(s rowScanner) (*AnomalyFinding, error) {
	var (
		out        AnomalyFinding
		evidence   []byte
		resolvedBy *uuid.UUID
		resolvedAt *time.Time
	)
	if err := s.Scan(
		&out.ID, &out.TenantID, &out.ReportID, &out.DocumentID, &out.AnomalyType,
		&out.Severity, &out.Description, &evidence,
		&out.ZScore, &out.SimilarityScore,
		&out.Status, &resolvedBy, &resolvedAt, &out.ResolutionNote,
		&out.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	out.Evidence = json.RawMessage(evidence)
	out.ResolvedBy = resolvedBy
	out.ResolvedAt = resolvedAt
	return &out, nil
}

func scanAnomalyConfig(s rowScanner) (*AnomalyConfig, error) {
	var c AnomalyConfig
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.ScheduleCron, &c.ZScoreThreshold,
		&c.ContentDistanceThreshold, &c.MinDocumentsForAnalysis,
		&c.AnalyzeMetadata, &c.AnalyzeContent, &c.AnalyzeBehavioral,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}
