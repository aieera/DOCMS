// ADR 0069 — federated-search persistence + admin allow-list.
//
// Two responsibilities on the repo:
//   1. IsPlatformAdmin    — fast O(1) check used as the perm gate
//   2. RecordFederatedAudit — write the audit row (success or denial)
//
// Both sit OUTSIDE the tenant RLS scope — platform_admins is the
// source of truth for who can bypass tenants, and the audit table
// records cross-tenant calls, so neither belongs under tenant_id.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// IsPlatformAdmin returns true when the user has an active grant
// (revoked_at IS NULL) in platform_admins. Cheap query on a
// partial index. Errors bubble up — the handler must distinguish
// "not in list" (return false) from "DB error" (5xx).
func (r *Repository) IsPlatformAdmin(ctx context.Context, userID string) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT 1 FROM platform_admins
		 WHERE user_id = $1 AND revoked_at IS NULL
		 LIMIT 1
	`, userID).Scan(&n)
	if err != nil {
		// Missing row → not an admin, NOT a DB error. The handler
		// returns 403 with an audit row in that case.
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return n == 1, nil
}

// FederatedAuditRow is the insert shape for the audit table.
type FederatedAuditRow struct {
	CallerID        string
	Reason          string
	QueryPayload    map[string]any
	ResultsSummary  map[string]any
	LatencyMS       int
	Outcome         string // 'success' | 'denied_perm' | 'denied_quota' | 'error'
	ErrorKind       string
}

// RecordFederatedAudit writes one row. Always succeeds-or-errors —
// the handler treats a write failure as an internal error and 5xxs,
// because a federated search that DOESN'T audit is worse than one
// that fails outright (privacy review trumps usability).
func (r *Repository) RecordFederatedAudit(ctx context.Context, row FederatedAuditRow) error {
	if row.Outcome == "" {
		row.Outcome = "success"
	}
	queryJSON, err := json.Marshal(row.QueryPayload)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(row.ResultsSummary)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO federated_search_audit
		    (caller_id, reason, query_payload, results_summary,
		     latency_ms, outcome, error_kind, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), now())
	`, row.CallerID, row.Reason, queryJSON, summaryJSON,
		row.LatencyMS, row.Outcome, row.ErrorKind)
	return err
}

// CountFederatedQueriesToday returns the caller's count for today
// (UTC). Used by the rate limiter; the source-of-truth is the
// audit row count, not a separate Redis counter, so a Redis flush
// can't open a backdoor to bypass the daily cap.
func (r *Repository) CountFederatedQueriesToday(ctx context.Context, callerID string) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM federated_search_audit
		 WHERE caller_id = $1
		   AND created_at >= date_trunc('day', now() at time zone 'UTC')
		   AND outcome = 'success'
	`, callerID).Scan(&n)
	return n, err
}

// ListRecentFederatedAudit returns the caller's recent rows for the
// admin's own UI. Cap defaults to 20.
func (r *Repository) ListRecentFederatedAudit(ctx context.Context, callerID string, limit int) ([]FederatedAuditRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, reason, query_payload, results_summary,
		       latency_ms, outcome, COALESCE(error_kind, ''), created_at
		  FROM federated_search_audit
		 WHERE caller_id = $1
		 ORDER BY created_at DESC
		 LIMIT $2
	`, callerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FederatedAuditRecord
	for rows.Next() {
		var rec FederatedAuditRecord
		var qb, sb []byte
		if err := rows.Scan(&rec.ID, &rec.Reason, &qb, &sb,
			&rec.LatencyMS, &rec.Outcome, &rec.ErrorKind, &rec.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(qb, &rec.QueryPayload)
		_ = json.Unmarshal(sb, &rec.ResultsSummary)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// FederatedAuditRecord is the read-back shape — the caller's recent
// queries, used by the admin UI to surface their own activity.
type FederatedAuditRecord struct {
	ID             string         `json:"id"`
	Reason         string         `json:"reason"`
	QueryPayload   map[string]any `json:"query_payload"`
	ResultsSummary map[string]any `json:"results_summary"`
	LatencyMS      int            `json:"latency_ms"`
	Outcome        string         `json:"outcome"`
	ErrorKind      string         `json:"error_kind,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}
