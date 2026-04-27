package handler

// security_scan_handler.go — POST ingest path for CI security-gate
// results, plus a GET latest-per-type for the admin UI.
//
// Trust model: /internal/v1/audit/security-scans is mounted on the
// internal tier (mTLS + HMAC per ADR 0031) because the caller is the
// CI workflow, not a tenant user. The read endpoint
// /api/v1/platform/security/posture lives on the workflow service
// and aggregates from here; see services/workflow/internal/handler/
// platform_handler.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ScanStatus enumerates the accepted values on the DB CHECK
// constraint. Keeping them as typed constants avoids the classic
// "pass"/"passed" typo that CI would propagate forever.
type ScanStatus string

const (
	StatusPass ScanStatus = "pass"
	StatusFail ScanStatus = "fail"
)

// ScanType mirrors the four ADR 0033 gates.
type ScanType string

const (
	ScanTypeSAST       ScanType = "sast"
	ScanTypeDepScan    ScanType = "dep_scan"
	ScanTypeDAST       ScanType = "dast"
	ScanTypeSecretScan ScanType = "secret_scan"
)

// SecurityScanBody is the JSON shape the CI workflow POSTs. Counters
// are optional — tools that don't produce severity breakdowns
// (gitleaks) leave them at zero.
type SecurityScanBody struct {
	ScanType      string     `json:"scan_type"`
	Status        string     `json:"status"`
	CriticalCount int        `json:"critical_count,omitempty"`
	HighCount     int        `json:"high_count,omitempty"`
	MediumCount   int        `json:"medium_count,omitempty"`
	LowCount      int        `json:"low_count,omitempty"`
	RunID         string     `json:"run_id,omitempty"`
	RunURL        string     `json:"run_url,omitempty"`
	CommitSHA     string     `json:"commit_sha,omitempty"`
	Branch        string     `json:"branch,omitempty"`
	RanAt         *time.Time `json:"ran_at,omitempty"`
}

// SecurityScanView is the latest-row shape the GET endpoint returns
// and the platform posture aggregator consumes.
type SecurityScanView struct {
	ScanType      string    `json:"scan_type"`
	Status        string    `json:"status"`
	CriticalCount int       `json:"critical_count"`
	HighCount     int       `json:"high_count"`
	MediumCount   int       `json:"medium_count"`
	LowCount      int       `json:"low_count"`
	RunID         string    `json:"run_id,omitempty"`
	RunURL        string    `json:"run_url,omitempty"`
	CommitSHA     string    `json:"commit_sha,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	RanAt         time.Time `json:"ran_at"`
}

// SecurityScanHandler owns the DB connection. Built separately from
// the main audit Handler so the audit handler can stay narrow — this
// is a platform-admin concern, not a tenant audit concern.
type SecurityScanHandler struct {
	pool *pgxpool.Pool
}

// NewSecurityScanHandler constructs a handler against the shared pool.
// pool must be non-nil; a nil pool would panic at first request.
func NewSecurityScanHandler(pool *pgxpool.Pool) *SecurityScanHandler {
	return &SecurityScanHandler{pool: pool}
}

// Register mounts the two routes. Write is under /internal/ so the
// gateway doesn't proxy it to end users; read is under /api/ so the
// workflow service's platform aggregator can call it (or an operator
// can curl it directly during an incident).
func (h *SecurityScanHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/audit/security-scans", h.ingest)
	mux.HandleFunc("GET /api/v1/audit/security-scans/latest", h.latest)
}

func (h *SecurityScanHandler) ingest(w http.ResponseWriter, r *http.Request) {
	var body SecurityScanBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !isScanType(body.ScanType) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid scan_type %q", body.ScanType))
		return
	}
	if body.Status != string(StatusPass) && body.Status != string(StatusFail) {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid status %q (want pass|fail)", body.Status))
		return
	}

	ran := time.Now().UTC()
	if body.RanAt != nil {
		ran = body.RanAt.UTC()
	}

	_, err := h.pool.Exec(r.Context(), `
		INSERT INTO security_scans (
			scan_type, status,
			critical_count, high_count, medium_count, low_count,
			run_id, run_url, commit_sha, branch, ran_at
		) VALUES (
			$1, $2,
			$3, $4, $5, $6,
			NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), NULLIF($10, ''), $11
		)`,
		body.ScanType, body.Status,
		body.CriticalCount, body.HighCount, body.MediumCount, body.LowCount,
		body.RunID, body.RunURL, body.CommitSHA, body.Branch, ran,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

// latest returns the most recent row per scan_type. The aggregator in
// workflow/platform calls this and joins into a single posture view;
// operators can also curl it directly for quick triage.
func (h *SecurityScanHandler) latest(w http.ResponseWriter, r *http.Request) {
	// DISTINCT ON (scan_type) ORDER BY ran_at DESC returns exactly one
	// row per scan type — the newest. Fast via the type+ran_at index.
	rows, err := h.pool.Query(r.Context(), `
		SELECT DISTINCT ON (scan_type)
		       scan_type, status,
		       critical_count, high_count, medium_count, low_count,
		       COALESCE(run_id, ''), COALESCE(run_url, ''),
		       COALESCE(commit_sha, ''), COALESCE(branch, ''),
		       ran_at
		  FROM security_scans
		 ORDER BY scan_type, ran_at DESC`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	out := make([]SecurityScanView, 0, 4)
	for rows.Next() {
		var v SecurityScanView
		if err := rows.Scan(
			&v.ScanType, &v.Status,
			&v.CriticalCount, &v.HighCount, &v.MediumCount, &v.LowCount,
			&v.RunID, &v.RunURL, &v.CommitSHA, &v.Branch,
			&v.RanAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"scans": out})
}

func isScanType(s string) bool {
	switch ScanType(s) {
	case ScanTypeSAST, ScanTypeDepScan, ScanTypeDAST, ScanTypeSecretScan:
		return true
	}
	return false
}
