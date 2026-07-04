package records

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// Records-management certification: a catalog of standards (DoD 5015.2, ISO
// 15489), each a list of requirements mapped to the SeDoc mechanism that
// satisfies it plus a live pass/fail/attested check. Coverage evaluates the
// catalog against a tenant's records data; the evidence report bundles the
// mapping + coverage + stats for the one-click certification pack (the
// frontend merges the audit verify-integrity proof in).

// Requirement maps one standard clause to a SeDoc mechanism + a live check.
type Requirement struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Mechanism is the SeDoc feature that satisfies the requirement — the
	// "requirement → mechanism" mapping the DoD asks for.
	Mechanism string `json:"mechanism"`
	// CheckKey selects the runtime evaluation (see evaluate). "capability"
	// means SeDoc provides the mechanism (attested); the rest are data checks.
	CheckKey string `json:"check_key"`
}

// Standard is one certifiable records-management standard.
type Standard struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	Requirements []Requirement `json:"requirements"`
}

// RequirementResult is a requirement plus its evaluated status.
type RequirementResult struct {
	Requirement
	Status string `json:"status"` // pass | fail | attested
	Detail string `json:"detail,omitempty"`
}

// RecordsStats is the live records posture coverage checks read from.
type RecordsStats struct {
	Categories               int `json:"categories"`
	Series                   int `json:"series"`
	Schedules                int `json:"schedules"`
	RecordsTotal             int `json:"records_total"`
	RecordsActive            int `json:"records_active"` // declared|cutoff_pending
	RecordsDisposed          int `json:"records_disposed"`
	RecordsTransferred       int `json:"records_transferred"`
	Vital                    int `json:"vital"`
	Frozen                   int `json:"frozen"`
	RecordsMissingSchedule   int `json:"records_missing_schedule"`
	RecordsMissingMetadata   int `json:"records_missing_metadata"`
	DisposedMissingCertified int `json:"disposed_missing_certified"`
}

// CoverageReport is the per-standard checklist + posture for a tenant.
type CoverageReport struct {
	StandardID   string              `json:"standard_id"`
	StandardName string              `json:"standard_name"`
	GeneratedAt  time.Time           `json:"generated_at"`
	Total        int                 `json:"total"`
	Passed       int                 `json:"passed"`
	Attested     int                 `json:"attested"`
	Gaps         int                 `json:"gaps"`
	CoveragePct  int                 `json:"coverage_pct"`
	Requirements []RequirementResult `json:"requirements"`
	Stats        RecordsStats        `json:"stats"`
}

// mandatoryMetadataKeys are the record-metadata fields a declared record must
// carry to pass the mandatory-metadata check (DoD-style minimal profile).
var mandatoryMetadataKeys = []string{"declaring_agent", "originating_organization", "security_classification"}

var standards = map[string]Standard{
	"dod5015.2": {
		ID:          "dod5015.2",
		Name:        "DoD 5015.2",
		Description: "US DoD Electronic Records Management Software Applications Design Criteria Standard.",
		Requirements: []Requirement{
			{"C2.2.3-fileplan", "File plan (categories/series)", "A hierarchical file plan classifies records into categories and series.", "records.RecordCategory tree (record_categories)", "file_plan_exists"},
			{"C2.2.3-schedules", "Retention schedules", "Each series carries a retention schedule (trigger + period + disposition).", "records.RetentionSchedule attached to plan nodes", "schedules_exist"},
			{"C2.2.6-declare", "Record declaration", "Documents can be declared as records and become immutable.", "records.Declare + DocumentService immutability gate (ErrRecordDeclared)", "capability"},
			{"C2.2.10-metadata", "Mandatory record metadata", "Declared records carry mandatory metadata (agent, originating org, security class).", "records.metadata + mandatory-metadata check", "mandatory_metadata"},
			{"C2.2.8-cutoff", "Cutoff & disposition", "Records reach cutoff per schedule and proceed to disposition.", "records cutoff sweep + records.Dispose", "records_have_schedule"},
			{"C2.2.9-certified", "Certified disposition", "Disposition is certified and recorded with a certificate handle.", "records.Dispose (certify) + disposition_event_id", "disposition_certified"},
			{"C2.2.12-vital", "Vital records program", "Vital records can be designated and reported.", "records.vital_record flag + vital report", "capability"},
			{"C2.2.7-freeze", "Freeze / unfreeze", "Records can be frozen (halting disposition) independent of legal hold.", "records.Freeze / Unfreeze", "capability"},
			{"C2.2.11-transfer", "Transfer / accession export", "Records can be exported for transfer/accession to a receiving archive.", "records.AccessionExport (transfer manifest)", "capability"},
			{"C2.2.5-audittrail", "Audit trail completeness", "All record actions emit auditable events.", "dms.record.* outbox events (declared/disposed/frozen/unfrozen/vital/transfer)", "capability"},
			{"C2.2.5-auditintegrity", "Audit integrity (tamper-evident)", "The audit trail is tamper-evident and verifiable.", "audit hash chain + signed checkpoints (verify-integrity)", "audit_integrity"},
		},
	},
	"iso15489": {
		ID:          "iso15489",
		Name:        "ISO 15489",
		Description: "International standard for records management principles and requirements.",
		Requirements: []Requirement{
			{"9.1-capture", "Records capture & declaration", "Records are captured/declared into the system.", "records.Declare", "capability"},
			{"9.2-classification", "Classification scheme", "A classification scheme (file plan) organizes records.", "records.RecordCategory tree", "file_plan_exists"},
			{"9.4-retention", "Retention & disposition authorities", "Documented retention authorities govern disposition.", "records.RetentionSchedule", "schedules_exist"},
			{"9.5-metadata", "Records metadata", "Records carry the metadata needed to manage them over time.", "records.metadata + mandatory-metadata check", "mandatory_metadata"},
			{"9.6-access", "Access & security", "Access to records is controlled and tenant-isolated.", "Postgres RLS + policy service permission checks", "capability"},
			{"9.7-vital", "Business continuity (vital records)", "Vital records supporting continuity are identified.", "records.vital_record flag", "capability"},
			{"9.8-disposition", "Authorized disposition", "Disposition is authorized and recorded.", "records.Dispose (certify) + disposition_event_id", "disposition_certified"},
			{"9.9-audit", "Monitoring & auditing", "Records actions are monitored via a verifiable audit trail.", "dms.record.* events + audit hash chain", "audit_integrity"},
		},
	},
}

// ListStandards returns the available standards (id + name + requirement count).
func ListStandards() []map[string]any {
	out := []map[string]any{}
	for _, id := range []string{"dod5015.2", "iso15489"} {
		s := standards[id]
		out = append(out, map[string]any{
			"id": s.ID, "name": s.Name, "description": s.Description, "requirements": len(s.Requirements),
		})
	}
	return out
}

// Coverage evaluates a standard's requirements against the tenant's records.
func (s *Service) Coverage(ctx context.Context, tenantID uuid.UUID, standardID string) (*CoverageReport, error) {
	std, ok := standards[standardID]
	if !ok {
		return nil, vdmserr.NotFound("unknown standard")
	}
	stats, err := s.stats(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	rep := &CoverageReport{
		StandardID: std.ID, StandardName: std.Name, GeneratedAt: time.Now().UTC(),
		Requirements: make([]RequirementResult, 0, len(std.Requirements)), Stats: stats,
	}
	for _, req := range std.Requirements {
		status, detail := evaluate(req.CheckKey, stats)
		rep.Requirements = append(rep.Requirements, RequirementResult{Requirement: req, Status: status, Detail: detail})
		switch status {
		case "pass":
			rep.Passed++
		case "attested":
			rep.Attested++
		case "fail":
			rep.Gaps++
		}
	}
	rep.Total = len(std.Requirements)
	if rep.Total > 0 {
		rep.CoveragePct = (rep.Passed + rep.Attested) * 100 / rep.Total
	}
	return rep, nil
}

// evaluate runs one requirement check against the records stats. "capability"
// is attested (SeDoc provides the mechanism); data checks pass/fail on posture.
func evaluate(key string, st RecordsStats) (string, string) {
	switch key {
	case "capability":
		return "attested", "mechanism present in SeDoc"
	case "audit_integrity":
		// Cryptographic proof comes from the audit service; the frontend
		// overlays the live verify-integrity result into the evidence pack.
		return "attested", "verify via /api/v1/audit/verify-integrity (hash chain + signed checkpoints)"
	case "file_plan_exists":
		if st.Categories > 0 {
			return "pass", "file plan has nodes"
		}
		return "fail", "no file-plan categories defined"
	case "schedules_exist":
		if st.Schedules > 0 {
			return "pass", "retention schedules defined"
		}
		return "fail", "no retention schedules defined"
	case "records_have_schedule":
		if st.RecordsActive == 0 {
			return "attested", "no active records yet"
		}
		if st.RecordsMissingSchedule == 0 {
			return "pass", "every active record has a retention schedule"
		}
		return "fail", plural(st.RecordsMissingSchedule, "active record") + " without a retention schedule"
	case "mandatory_metadata":
		if st.RecordsActive == 0 {
			return "attested", "no active records yet"
		}
		if st.RecordsMissingMetadata == 0 {
			return "pass", "every active record has the mandatory metadata"
		}
		return "fail", plural(st.RecordsMissingMetadata, "active record") + " missing mandatory metadata"
	case "disposition_certified":
		if st.RecordsDisposed+st.RecordsTransferred == 0 {
			return "attested", "no dispositions yet (mechanism present)"
		}
		if st.DisposedMissingCertified == 0 {
			return "pass", "every disposition has a certificate"
		}
		return "fail", plural(st.DisposedMissingCertified, "disposed record") + " without a certificate"
	default:
		return "attested", ""
	}
}

// stats gathers the live records posture in one tenant transaction.
func (s *Service) stats(ctx context.Context, tenantID uuid.UUID) (RecordsStats, error) {
	var st RecordsStats
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM record_categories WHERE tenant_id=$1),
			(SELECT count(*) FROM record_categories WHERE tenant_id=$1 AND node_type='series'),
			(SELECT count(*) FROM retention_schedules WHERE tenant_id=$1)`,
			tenantID).Scan(&st.Categories, &st.Series, &st.Schedules); err != nil {
			return vdmserr.FromPgError(err)
		}
		// Build the mandatory-metadata predicate: a record is "missing" if any
		// required key is absent from its metadata jsonb. jsonb_exists (not the
		// `?` operator) avoids any pgx placeholder ambiguity; keys are a fixed
		// allow-list constant, so the concatenation is injection-safe.
		missingMeta := ""
		for _, k := range mandatoryMetadataKeys {
			if missingMeta != "" {
				missingMeta += " OR "
			}
			missingMeta += "NOT jsonb_exists(metadata, '" + k + "')"
		}
		if err := tx.QueryRow(ctx, `SELECT
			count(*),
			count(*) FILTER (WHERE disposition_state IN ('declared','cutoff_pending')),
			count(*) FILTER (WHERE disposition_state='disposed'),
			count(*) FILTER (WHERE disposition_state='transferred'),
			count(*) FILTER (WHERE vital_record),
			count(*) FILTER (WHERE frozen),
			count(*) FILTER (WHERE disposition_state IN ('declared','cutoff_pending') AND retention_schedule_id IS NULL),
			count(*) FILTER (WHERE disposition_state IN ('declared','cutoff_pending') AND (`+missingMeta+`)),
			count(*) FILTER (WHERE disposition_state IN ('disposed','transferred') AND disposition_event_id IS NULL)
			FROM records WHERE tenant_id=$1`,
			tenantID).Scan(&st.RecordsTotal, &st.RecordsActive, &st.RecordsDisposed,
			&st.RecordsTransferred, &st.Vital, &st.Frozen, &st.RecordsMissingSchedule,
			&st.RecordsMissingMetadata, &st.DisposedMissingCertified); err != nil {
			return vdmserr.FromPgError(err)
		}
		return nil
	})
	return st, err
}

func plural(n int, noun string) string {
	s := strconv.Itoa(n) + " " + noun
	if n != 1 {
		s += "s"
	}
	return s
}
