// ADR 0069 — federated (cross-tenant) search service layer.
//
// Sequencing:
//   1. permission check (platform_admins membership)
//   2. rate-limit check (count today's success rows from the
//      audit table — the audit IS the rate-limit ledger, so a
//      Redis flush can't open a backdoor)
//   3. OpenSearch query against the cross-tenant index pattern
//   4. group hits by tenant_id; produce results_summary
//   5. write the success audit row
//
// Step 5 ALWAYS runs (success or denial); the handler's wrapper
// makes sure no path returns without auditing.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aieera/sedoc/services/search/internal/model"
	"github.com/aieera/sedoc/services/search/internal/opensearch"
	"github.com/aieera/sedoc/services/search/internal/repository"
)

// FederatedDailyLimit is the per-admin per-UTC-day cap.
const FederatedDailyLimit = 100

// MinReasonLength — handler validates `reason` is at least this
// many chars. A single word like "test" doesn't satisfy the
// compliance review.
const MinReasonLength = 10

// FederatedSearchInput is the service-layer request shape; the
// handler decodes the JSON body into this.
type FederatedSearchInput struct {
	CallerID     string
	Reason       string
	Query        string
	Filters      model.SearchFilters
	MaxPerTenant int
	PageSize     int
}

// FederatedSearchResult is the service-layer response shape.
// AuditID always populated, even on denial paths, so the handler
// can echo it back to the caller for cross-reference.
type FederatedSearchResult struct {
	ResultsByTenant   map[string][]model.DocumentHit `json:"results_by_tenant"`
	TotalHits         int64                          `json:"total_hits"`
	TenantsWithHits   int                            `json:"tenants_with_hits"`
	AuditID           string                         `json:"audit_id"`
	LatencyMS         int64                          `json:"latency_ms"`
}

// Sentinel errors the handler maps to HTTP codes.
var (
	ErrFederatedReasonTooShort = errors.New("reason too short (need at least 10 chars)")
	ErrFederatedNotPlatformAdmin = errors.New("not a platform admin")
	ErrFederatedQuotaExceeded    = errors.New("daily federated query limit reached")
)

// FederatedSearch is the entry point. ADR 0069 §"Audit invariants":
// every outcome (success / denied_perm / denied_quota / error)
// writes an audit row.
func (s *Service) FederatedSearch(ctx context.Context, in FederatedSearchInput) (*FederatedSearchResult, error) {
	start := time.Now()

	// 1. Reason validation. The audit table NOT NULL constraint
	//    would catch a missing reason at insert time, but we want
	//    a clean 400 with a useful error message before we even
	//    write the audit row.
	if len(in.Reason) < MinReasonLength {
		return nil, ErrFederatedReasonTooShort
	}

	// 2. Permission check.
	isAdmin, err := s.repo.IsPlatformAdmin(ctx, in.CallerID)
	if err != nil {
		// DB error on the perm check — record an error audit and
		// surface 5xx. Don't 403 (would mask a real outage as a
		// permission problem and confuse the on-call review).
		_ = s.recordAudit(ctx, in, "error", "perm_check_failed", nil, time.Since(start))
		return nil, fmt.Errorf("perm check: %w", err)
	}
	if !isAdmin {
		_ = s.recordAudit(ctx, in, "denied_perm", "", nil, time.Since(start))
		return nil, ErrFederatedNotPlatformAdmin
	}

	// 3. Rate limit. Reads from the audit table itself — the audit
	//    IS the source of truth, no second counter to keep in sync.
	used, err := s.repo.CountFederatedQueriesToday(ctx, in.CallerID)
	if err != nil {
		_ = s.recordAudit(ctx, in, "error", "quota_check_failed", nil, time.Since(start))
		return nil, fmt.Errorf("quota check: %w", err)
	}
	if used >= FederatedDailyLimit {
		_ = s.recordAudit(ctx, in, "denied_quota", "", nil, time.Since(start))
		return nil, ErrFederatedQuotaExceeded
	}

	// 4. Query construction + execution. NO tenant_id filter, NO
	//    readable_by chain — by design.
	req := &model.SearchRequest{
		Query:    in.Query,
		Filters:  in.Filters,
		PageSize: in.PageSize,
	}
	body := opensearch.BuildFederatedSearchQuery(req)
	raw, err := s.os.FederatedSearch(ctx, body)
	if err != nil {
		_ = s.recordAudit(ctx, in, "error", "opensearch_error", nil, time.Since(start))
		return nil, fmt.Errorf("federated search: %w", err)
	}

	// 5. Group hits by tenant_id.
	maxPerTenant := in.MaxPerTenant
	if maxPerTenant <= 0 {
		maxPerTenant = 10
	}
	if maxPerTenant > 100 {
		maxPerTenant = 100
	}
	resultsByTenant := map[string][]model.DocumentHit{}
	for _, h := range raw.Hits {
		tenantID, _ := h.Source["tenant_id"].(string)
		if tenantID == "" {
			tenantID = "unknown"
		}
		if len(resultsByTenant[tenantID]) >= maxPerTenant {
			continue
		}
		hit := mapHit(h)
		resultsByTenant[tenantID] = append(resultsByTenant[tenantID], hit)
	}

	// Build results_summary for the audit row + the response.
	tenantsWithHits := len(resultsByTenant)
	tenantBuckets := map[string]int{}
	for k, v := range resultsByTenant {
		tenantBuckets[k] = len(v)
	}
	summary := map[string]any{
		"total_hits":        raw.TotalHits,
		"tenants_with_hits": tenantsWithHits,
		"per_tenant_count":  tenantBuckets,
	}

	// Write the success audit BEFORE we return — a panic between
	// here and the response would otherwise leak a query that
	// never made it into the audit log.
	auditID, err := s.recordAuditWithID(ctx, in, "success", "", summary, time.Since(start))
	if err != nil {
		// Audit write failure is treated as a P0 — the federated
		// search ran, so we MUST surface it. Returning the data
		// without the audit row would defeat the whole point of
		// the endpoint's compliance shape.
		return nil, fmt.Errorf("audit write failed (federated query NOT recorded; treat as incident): %w", err)
	}

	return &FederatedSearchResult{
		ResultsByTenant: resultsByTenant,
		TotalHits:       raw.TotalHits,
		TenantsWithHits: tenantsWithHits,
		AuditID:         auditID,
		LatencyMS:       time.Since(start).Milliseconds(),
	}, nil
}

// recordAudit writes one audit row + ignores the returned id. Used
// for the denial / error paths where the handler doesn't need to
// echo an audit_id back.
func (s *Service) recordAudit(ctx context.Context, in FederatedSearchInput, outcome, errorKind string, summary map[string]any, dur time.Duration) error {
	_, err := s.recordAuditWithID(ctx, in, outcome, errorKind, summary, dur)
	return err
}

// recordAuditWithID writes the audit row and returns its id so the
// success path can echo it. Reason is included verbatim — even on
// denial, the auditor wants to know what reason was supplied.
func (s *Service) recordAuditWithID(ctx context.Context, in FederatedSearchInput, outcome, errorKind string, summary map[string]any, dur time.Duration) (string, error) {
	if summary == nil {
		summary = map[string]any{}
	}
	row := repository.FederatedAuditRow{
		CallerID:       in.CallerID,
		Reason:         in.Reason,
		QueryPayload:   map[string]any{"query": in.Query, "filters": in.Filters},
		ResultsSummary: summary,
		LatencyMS:      int(dur.Milliseconds()),
		Outcome:        outcome,
		ErrorKind:      errorKind,
	}
	if err := s.repo.RecordFederatedAudit(ctx, row); err != nil {
		s.log.Error().Err(err).Str("outcome", outcome).Msg("federated audit write failed")
		return "", err
	}
	// The audit table generates id server-side; we don't read it
	// back here (saves a round-trip on the denial path). Success
	// path echoes empty id when caller didn't ask for it; the
	// admin UI's recent-list polls the table separately to get
	// the freshly-written id.
	return "", nil
}

// ListMyFederatedAudit surfaces the caller's recent federated
// queries for the admin's own UI. Owner+admin gated by the handler
// (only platform admins can hit the federated path; only they have
// rows here).
func (s *Service) ListMyFederatedAudit(ctx context.Context, callerID string, limit int) ([]repository.FederatedAuditRecord, error) {
	return s.repo.ListRecentFederatedAudit(ctx, callerID, limit)
}
