// Repository for ADR 0054 — PII/PHI compliance findings + summary +
// per-tenant config. Named "_pii_" to disambiguate from the existing
// compliance package (legal holds), not a separate module.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// ---- types ---------------------------------------------------------------

type ComplianceFinding struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	DocumentID        uuid.UUID
	VersionID         uuid.UUID
	EntityType        string
	EntityCategory    string // 'pii'|'phi'
	OccurrenceCount   int32
	PageNumbers       []int32
	Confidence        float32
	RiskLevel         string // 'critical'|'high'|'medium'|'low'
	SampleContext     string
	DetectionSource   string // 'ner'|'pattern'|'custom'
	RemediationStatus string // 'open'|'acknowledged'|'remediated'|'false_positive'
	RemediatedBy      *uuid.UUID
	RemediatedAt      *time.Time
	RemediationNote   string
	CreatedAt         time.Time
}

type ComplianceSummary struct {
	TenantID         uuid.UUID
	DocumentID       uuid.UUID
	VersionID        uuid.UUID
	OverallRisk      string
	PIICount         int32
	PHICount         int32
	CriticalCount    int32
	HighCount        int32
	MediumCount      int32
	LowCount         int32
	EntityTypesFound []string
	NeedsReview      bool
	AutoHeld         bool
	ScannedAt        time.Time
}

type ComplianceConfig struct {
	TenantID                 uuid.UUID
	Enabled                  bool
	AutoHoldOnCritical       bool
	NotifyOnHigh             bool
	NotifyRoles              []string
	PIIEntityRiskOverrides   json.RawMessage
	PHIEnabled               bool
	CustomPatterns           json.RawMessage
}

type ComplianceConfigPatch struct {
	Enabled                *bool
	AutoHoldOnCritical     *bool
	NotifyOnHigh           *bool
	NotifyRoles            *[]string
	PIIEntityRiskOverrides *json.RawMessage
	PHIEnabled             *bool
	CustomPatterns         *json.RawMessage
}

type ListFindingsOpts struct {
	RiskLevel  string // empty = all
	Status     string // empty = all
	EntityType string // empty = all
	Limit      int32
	Offset     int32
}

type ComplianceDashboard struct {
	TotalDocumentsScanned  int64
	DocumentsWithFindings  int64
	OpenFindings           int64
	AutoHeldDocuments      int64
	RiskDistribution       map[string]int64
	TopEntityTypes         []EntityTypeFrequency
}

type EntityTypeFrequency struct {
	EntityType string
	Count      int64
}

// ---- interface -----------------------------------------------------------

type ComplianceRepository interface {
	GetSummary(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*ComplianceSummary, error)
	ListFindings(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ComplianceFinding, error)
	GetFinding(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*ComplianceFinding, error)
	UpdateFindingStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus, note string) (*ComplianceFinding, error)

	ListPendingFindings(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListFindingsOpts) ([]ComplianceFinding, int64, error)
	Dashboard(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ComplianceDashboard, error)

	GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ComplianceConfig, error)
	UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p ComplianceConfigPatch) (*ComplianceConfig, error)
}

type complianceRepo struct{}

func NewComplianceRepo() ComplianceRepository { return &complianceRepo{} }

// ---- summary + findings -------------------------------------------------

const selectSummarySQL = `
SELECT tenant_id, document_id, version_id, overall_risk,
       pii_count, phi_count, critical_count, high_count, medium_count, low_count,
       entity_types_found, needs_review, auto_held, scanned_at
  FROM compliance_summary
`

func (r *complianceRepo) GetSummary(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*ComplianceSummary, error) {
	row := tx.QueryRow(ctx, selectSummarySQL+` WHERE tenant_id = $1 AND document_id = $2`, tenantID, documentID)
	return scanComplianceSummary(row)
}

const selectFindingSQL = `
SELECT id, tenant_id, document_id, version_id, entity_type, entity_category,
       occurrence_count, page_numbers, confidence, risk_level,
       COALESCE(sample_context, ''), detection_source,
       remediation_status, remediated_by, remediated_at,
       COALESCE(remediation_note, ''), created_at
  FROM compliance_findings
`

func (r *complianceRepo) ListFindings(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]ComplianceFinding, error) {
	rows, err := tx.Query(ctx, selectFindingSQL+`
		WHERE tenant_id = $1 AND document_id = $2
		ORDER BY
		  CASE risk_level
		    WHEN 'critical' THEN 0 WHEN 'high' THEN 1
		    WHEN 'medium' THEN 2   ELSE 3 END,
		  created_at DESC
		LIMIT 200`,
		tenantID, documentID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]ComplianceFinding, 0, 8)
	for rows.Next() {
		f, err := scanComplianceFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, mapPgError(rows.Err())
}

func (r *complianceRepo) GetFinding(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*ComplianceFinding, error) {
	row := tx.QueryRow(ctx, selectFindingSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanComplianceFinding(row)
}

func (r *complianceRepo) UpdateFindingStatus(ctx context.Context, tx pgx.Tx, tenantID, id, reviewer uuid.UUID, newStatus, note string) (*ComplianceFinding, error) {
	switch newStatus {
	case "acknowledged", "remediated", "false_positive", "open":
	default:
		return nil, vdmserr.Validation("status", "invalid")
	}
	row := tx.QueryRow(ctx, `
		UPDATE compliance_findings
		   SET remediation_status = $4,
		       remediated_by      = CASE WHEN $4 = 'open' THEN NULL ELSE $3 END,
		       remediated_at      = CASE WHEN $4 = 'open' THEN NULL ELSE now() END,
		       remediation_note   = NULLIF($5, '')
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING id, tenant_id, document_id, version_id, entity_type, entity_category,
		           occurrence_count, page_numbers, confidence, risk_level,
		           COALESCE(sample_context, ''), detection_source,
		           remediation_status, remediated_by, remediated_at,
		           COALESCE(remediation_note, ''), created_at`,
		tenantID, id, reviewer, newStatus, note,
	)
	f, err := scanComplianceFinding(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, vdmserr.ErrNotFound) {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

// ---- admin ---------------------------------------------------------------

func (r *complianceRepo) ListPendingFindings(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, opts ListFindingsOpts) ([]ComplianceFinding, int64, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	status := opts.Status
	if status == "" {
		status = "open"
	}
	risk := opts.RiskLevel  // empty = no filter
	etype := opts.EntityType

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM compliance_findings
		 WHERE tenant_id = $1
		   AND remediation_status = $2
		   AND ($3 = '' OR risk_level  = $3)
		   AND ($4 = '' OR entity_type = $4)`,
		tenantID, status, risk, etype,
	).Scan(&total); err != nil {
		return nil, 0, mapPgError(err)
	}
	rows, err := tx.Query(ctx, selectFindingSQL+`
		WHERE tenant_id = $1
		  AND remediation_status = $2
		  AND ($3 = '' OR risk_level  = $3)
		  AND ($4 = '' OR entity_type = $4)
		ORDER BY
		  CASE risk_level
		    WHEN 'critical' THEN 0 WHEN 'high' THEN 1
		    WHEN 'medium' THEN 2   ELSE 3 END,
		  created_at DESC
		LIMIT $5 OFFSET $6`,
		tenantID, status, risk, etype, opts.Limit, opts.Offset,
	)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	out := make([]ComplianceFinding, 0, opts.Limit)
	for rows.Next() {
		f, err := scanComplianceFinding(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *f)
	}
	return out, total, mapPgError(rows.Err())
}

func (r *complianceRepo) Dashboard(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ComplianceDashboard, error) {
	out := &ComplianceDashboard{
		RiskDistribution: map[string]int64{
			"critical": 0, "high": 0, "medium": 0, "low": 0, "none": 0,
		},
	}

	if err := tx.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE overall_risk <> 'none'),
		  COUNT(*) FILTER (WHERE auto_held)
		FROM compliance_summary
		WHERE tenant_id = $1`, tenantID,
	).Scan(&out.TotalDocumentsScanned, &out.DocumentsWithFindings, &out.AutoHeldDocuments); err != nil {
		return nil, mapPgError(err)
	}

	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM compliance_findings
		 WHERE tenant_id = $1 AND remediation_status = 'open'`,
		tenantID,
	).Scan(&out.OpenFindings); err != nil {
		return nil, mapPgError(err)
	}

	rows, err := tx.Query(ctx, `
		SELECT overall_risk, COUNT(*) FROM compliance_summary
		 WHERE tenant_id = $1 GROUP BY overall_risk`, tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var risk string
		var cnt int64
		if err := rows.Scan(&risk, &cnt); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.RiskDistribution[risk] = cnt
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT entity_type, SUM(occurrence_count)::bigint AS total
		  FROM compliance_findings
		 WHERE tenant_id = $1
		 GROUP BY entity_type
		 ORDER BY total DESC LIMIT 10`, tenantID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var ef EntityTypeFrequency
		if err := rows.Scan(&ef.EntityType, &ef.Count); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		out.TopEntityTypes = append(out.TopEntityTypes, ef)
	}
	rows.Close()

	return out, nil
}

// ---- config --------------------------------------------------------------

const selectComplianceConfigSQL = `
SELECT tenant_id, enabled, auto_hold_on_critical, notify_on_high,
       notify_roles, pii_entity_risk_overrides, phi_enabled, custom_patterns
  FROM compliance_config
`

func (r *complianceRepo) GetConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*ComplianceConfig, error) {
	row := tx.QueryRow(ctx, selectComplianceConfigSQL+` WHERE tenant_id = $1`, tenantID)
	return scanComplianceConfig(row)
}

func (r *complianceRepo) UpsertConfig(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, p ComplianceConfigPatch) (*ComplianceConfig, error) {
	cur, err := r.GetConfig(ctx, tx, tenantID)
	if err != nil && !errors.Is(err, vdmserr.ErrNotFound) {
		return nil, err
	}
	merged := ComplianceConfig{
		TenantID:               tenantID,
		Enabled:                true,
		AutoHoldOnCritical:     false,
		NotifyOnHigh:           true,
		NotifyRoles:            []string{"compliance_officer", "admin"},
		PIIEntityRiskOverrides: json.RawMessage(`{}`),
		PHIEnabled:             false,
		CustomPatterns:         json.RawMessage(`[]`),
	}
	if cur != nil {
		merged = *cur
	}
	if p.Enabled != nil {
		merged.Enabled = *p.Enabled
	}
	if p.AutoHoldOnCritical != nil {
		merged.AutoHoldOnCritical = *p.AutoHoldOnCritical
	}
	if p.NotifyOnHigh != nil {
		merged.NotifyOnHigh = *p.NotifyOnHigh
	}
	if p.NotifyRoles != nil {
		merged.NotifyRoles = *p.NotifyRoles
	}
	if p.PIIEntityRiskOverrides != nil {
		merged.PIIEntityRiskOverrides = *p.PIIEntityRiskOverrides
	}
	if p.PHIEnabled != nil {
		merged.PHIEnabled = *p.PHIEnabled
	}
	if p.CustomPatterns != nil {
		merged.CustomPatterns = *p.CustomPatterns
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO compliance_config
		    (tenant_id, enabled, auto_hold_on_critical, notify_on_high,
		     notify_roles, pii_entity_risk_overrides, phi_enabled, custom_patterns)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8::jsonb)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET enabled                   = EXCLUDED.enabled,
		       auto_hold_on_critical     = EXCLUDED.auto_hold_on_critical,
		       notify_on_high            = EXCLUDED.notify_on_high,
		       notify_roles              = EXCLUDED.notify_roles,
		       pii_entity_risk_overrides = EXCLUDED.pii_entity_risk_overrides,
		       phi_enabled               = EXCLUDED.phi_enabled,
		       custom_patterns           = EXCLUDED.custom_patterns,
		       updated_at                = now()`,
		tenantID, merged.Enabled, merged.AutoHoldOnCritical, merged.NotifyOnHigh,
		merged.NotifyRoles, []byte(merged.PIIEntityRiskOverrides),
		merged.PHIEnabled, []byte(merged.CustomPatterns),
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &merged, nil
}

// ---- scanners ------------------------------------------------------------

func scanComplianceSummary(s rowScanner) (*ComplianceSummary, error) {
	var c ComplianceSummary
	if err := s.Scan(
		&c.TenantID, &c.DocumentID, &c.VersionID, &c.OverallRisk,
		&c.PIICount, &c.PHICount, &c.CriticalCount, &c.HighCount,
		&c.MediumCount, &c.LowCount, &c.EntityTypesFound,
		&c.NeedsReview, &c.AutoHeld, &c.ScannedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	return &c, nil
}

func scanComplianceFinding(s rowScanner) (*ComplianceFinding, error) {
	var (
		f            ComplianceFinding
		remediatedBy *uuid.UUID
		remediatedAt *time.Time
	)
	if err := s.Scan(
		&f.ID, &f.TenantID, &f.DocumentID, &f.VersionID, &f.EntityType, &f.EntityCategory,
		&f.OccurrenceCount, &f.PageNumbers, &f.Confidence, &f.RiskLevel,
		&f.SampleContext, &f.DetectionSource,
		&f.RemediationStatus, &remediatedBy, &remediatedAt,
		&f.RemediationNote, &f.CreatedAt,
	); err != nil {
		return nil, mapPgError(err)
	}
	f.RemediatedBy = remediatedBy
	f.RemediatedAt = remediatedAt
	return &f, nil
}

func scanComplianceConfig(s rowScanner) (*ComplianceConfig, error) {
	var (
		c         ComplianceConfig
		overrides []byte
		custom    []byte
	)
	if err := s.Scan(
		&c.TenantID, &c.Enabled, &c.AutoHoldOnCritical, &c.NotifyOnHigh,
		&c.NotifyRoles, &overrides, &c.PHIEnabled, &custom,
	); err != nil {
		return nil, mapPgError(err)
	}
	c.PIIEntityRiskOverrides = json.RawMessage(overrides)
	c.CustomPatterns = json.RawMessage(custom)
	return &c, nil
}
