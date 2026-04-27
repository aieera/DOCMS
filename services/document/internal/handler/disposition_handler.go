// ADR 0036 — disposition queue admin endpoints.
//
//	GET    /api/v1/admin/disposition/candidates           list (filterable by status)
//	GET    /api/v1/admin/disposition/candidates/{id}      detail
//	POST   /api/v1/admin/disposition/candidates/{id}/approve
//	POST   /api/v1/admin/disposition/candidates/{id}/reject
//
// Approve/reject are gated to compliance_officer | owner per ADR 0036's
// separation-of-duties: admins author retention policies, compliance
// officers approve the destructions those policies queue. Owners can
// do both as a break-glass.
//
// The executor cron (see executor.go in slice 5) is the only path
// that advances 'approved' → 'executed'. This handler never destroys.

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// soakWindow is the minimum delay between approval and executor action.
// ADR 0036: 24h gives operations a chance to spot a bad policy run
// before destruction commits. Owner cannot shorten this — if they need
// immediate action they take the user-facing DELETE path.
const soakWindow = 24 * time.Hour

// DispositionHandler mounts the queue review endpoints.
type DispositionHandler struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewDispositionHandler constructs the handler. The outbox repo is
// required — every approve/reject row appends an audit event in the
// same tx that mutates the candidate row.
func NewDispositionHandler(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) *DispositionHandler {
	return &DispositionHandler{pool: pool, outbox: outbox, log: log}
}

// Register mounts routes on a mux. The trailing-slash form catches
// the {id} variants because Go 1.22 servemux doesn't auto-bridge them.
func (h *DispositionHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/disposition/candidates", h.list)
	mux.HandleFunc("GET /api/v1/admin/disposition/candidates/{id}", h.get)
	mux.HandleFunc("POST /api/v1/admin/disposition/candidates/{id}/approve", h.approve)
	mux.HandleFunc("POST /api/v1/admin/disposition/candidates/{id}/reject", h.reject)
}

// candidateDTO is the wire shape. document_title and policy_name are
// joined in for the review UI — the reviewer needs context, not just
// uuids.
type candidateDTO struct {
	ID              string     `json:"id"`
	DocumentID      string     `json:"document_id"`
	DocumentTitle   string     `json:"document_title,omitempty"`
	PolicyID        string     `json:"policy_id"`
	PolicyName      string     `json:"policy_name,omitempty"`
	ProposedAction  string     `json:"proposed_action"`
	Status          string     `json:"status"`
	ProposedAt      time.Time  `json:"proposed_at"`
	ReviewerID      string     `json:"reviewer_id,omitempty"`
	DecidedAt       *time.Time `json:"decided_at,omitempty"`
	DecidedReason   string     `json:"decided_reason,omitempty"`
	ExecuteAfter    *time.Time `json:"execute_after,omitempty"`
	ExecutedAt      *time.Time `json:"executed_at,omitempty"`
	ProposedBlobID  string     `json:"proposed_blob_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// requireReviewer gates admin-list reads (any admin role) AND
// approve/reject mutations (compliance_officer | owner only). The
// `mutating` flag picks the strict variant.
func (h *DispositionHandler) requireReviewer(w http.ResponseWriter, r *http.Request, mutating bool) (uuid.UUID, uuid.UUID, bool) {
	role := r.Header.Get("X-User-Role")
	var allowed bool
	if mutating {
		// ADR 0036: separation of duties. admin (the role that creates
		// retention policies) cannot solo-approve the destructions those
		// policies queue. compliance_officer is the dedicated reviewer;
		// owner is the break-glass.
		allowed = role == "compliance_officer" || role == "owner"
	} else {
		allowed = role == "compliance_officer" || role == "admin" || role == "owner"
	}
	if !allowed {
		writeProxyJSON(w, http.StatusForbidden, map[string]any{
			"type":    "FORBIDDEN",
			"message": "compliance_officer or owner role required",
		})
		return uuid.Nil, uuid.Nil, false
	}
	tenantID, err1 := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
	userID, err2 := uuid.Parse(r.Header.Get("X-User-ID"))
	if err1 != nil || err2 != nil || tenantID == uuid.Nil || userID == uuid.Nil {
		writeProxyJSON(w, http.StatusUnauthorized, map[string]any{
			"type": "UNAUTHORIZED", "message": "missing identity headers",
		})
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, userID, true
}

// list returns candidates filtered by status (default: 'queued').
func (h *DispositionHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.requireReviewer(w, r, false)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = "queued"
	case "queued", "approved", "rejected", "executed", "superseded", "all":
	default:
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type": "INVALID_ARGUMENT", "message": "invalid status",
		})
		return
	}

	out := []candidateDTO{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var rows pgx.Rows
		var err error
		base := `
			SELECT c.id, c.document_id, COALESCE(d.title, ''),
			       c.policy_id, COALESCE(p.name, ''),
			       c.proposed_action, c.status, c.proposed_at,
			       c.reviewer_id, c.decided_at, COALESCE(c.decided_reason, ''),
			       c.execute_after, c.executed_at, c.proposed_blob_id,
			       c.created_at, c.updated_at
			  FROM disposition_candidates c
			  LEFT JOIN documents d
			    ON d.tenant_id = c.tenant_id AND d.id = c.document_id
			  LEFT JOIN retention_policies p
			    ON p.tenant_id = c.tenant_id AND p.id = c.policy_id
			 WHERE c.tenant_id = $1`
		if status == "all" {
			rows, err = tx.Query(r.Context(), base+` ORDER BY c.proposed_at DESC LIMIT 500`, tenantID)
		} else {
			rows, err = tx.Query(r.Context(), base+` AND c.status = $2 ORDER BY c.proposed_at DESC LIMIT 500`, tenantID, status)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				dto          candidateDTO
				docID, polID uuid.UUID
				revID        *uuid.UUID
				proposedBlob *uuid.UUID
			)
			if err := rows.Scan(
				&dto.ID, &docID, &dto.DocumentTitle,
				&polID, &dto.PolicyName,
				&dto.ProposedAction, &dto.Status, &dto.ProposedAt,
				&revID, &dto.DecidedAt, &dto.DecidedReason,
				&dto.ExecuteAfter, &dto.ExecutedAt, &proposedBlob,
				&dto.CreatedAt, &dto.UpdatedAt,
			); err != nil {
				return err
			}
			dto.DocumentID = docID.String()
			dto.PolicyID = polID.String()
			if revID != nil {
				dto.ReviewerID = revID.String()
			}
			if proposedBlob != nil {
				dto.ProposedBlobID = proposedBlob.String()
			}
			out = append(out, dto)
		}
		return rows.Err()
	})
	if err != nil {
		h.log.Error().Err(err).Msg("disposition: list")
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// get returns a single candidate.
func (h *DispositionHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.requireReviewer(w, r, false)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"type": "INVALID_ARGUMENT", "message": "invalid id"})
		return
	}
	var dto candidateDTO
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var (
			docID, polID  uuid.UUID
			revID         *uuid.UUID
			proposedBlob  *uuid.UUID
		)
		err := tx.QueryRow(r.Context(), `
			SELECT c.id, c.document_id, COALESCE(d.title, ''),
			       c.policy_id, COALESCE(p.name, ''),
			       c.proposed_action, c.status, c.proposed_at,
			       c.reviewer_id, c.decided_at, COALESCE(c.decided_reason, ''),
			       c.execute_after, c.executed_at, c.proposed_blob_id,
			       c.created_at, c.updated_at
			  FROM disposition_candidates c
			  LEFT JOIN documents d
			    ON d.tenant_id = c.tenant_id AND d.id = c.document_id
			  LEFT JOIN retention_policies p
			    ON p.tenant_id = c.tenant_id AND p.id = c.policy_id
			 WHERE c.tenant_id = $1 AND c.id = $2
		`, tenantID, id).Scan(
			&dto.ID, &docID, &dto.DocumentTitle,
			&polID, &dto.PolicyName,
			&dto.ProposedAction, &dto.Status, &dto.ProposedAt,
			&revID, &dto.DecidedAt, &dto.DecidedReason,
			&dto.ExecuteAfter, &dto.ExecutedAt, &proposedBlob,
			&dto.CreatedAt, &dto.UpdatedAt,
		)
		if err != nil {
			return err
		}
		dto.DocumentID = docID.String()
		dto.PolicyID = polID.String()
		if revID != nil {
			dto.ReviewerID = revID.String()
		}
		if proposedBlob != nil {
			dto.ProposedBlobID = proposedBlob.String()
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeProxyJSON(w, http.StatusNotFound, map[string]any{"type": "NOT_FOUND", "message": "candidate not found"})
		return
	}
	if err != nil {
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, dto)
}

type decideBody struct {
	Reason string `json:"reason"`
}

// approve flips status queued → approved, sets reviewer + decided_at,
// stamps execute_after = now() + soakWindow. The executor cron picks
// it up after the soak window elapses.
//
// Refuses if the document is now on legal hold (a hold can be applied
// after the candidate was queued; we re-check at every state advance).
func (h *DispositionHandler) approve(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.requireReviewer(w, r, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"type": "INVALID_ARGUMENT", "message": "invalid id"})
		return
	}
	body := decideBody{}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	executeAt := time.Now().UTC().Add(soakWindow)
	var docID uuid.UUID
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		// Re-check legal hold at approval time. A hold applied after
		// the candidate was queued must block the approval —
		// destruction-of-held-data is the failure mode this whole
		// queue exists to prevent.
		err := tx.QueryRow(r.Context(), `
			SELECT c.document_id
			  FROM disposition_candidates c
			  JOIN documents d
			    ON d.tenant_id = c.tenant_id AND d.id = c.document_id
			 WHERE c.tenant_id = $1 AND c.id = $2
			   AND c.status = 'queued'
			   AND NOT (d.under_legal_hold OR d.hold_count > 0)
			   AND d.deleted_at IS NULL
		`, tenantID, id).Scan(&docID)
		if err != nil {
			return err
		}
		// UPDATE with WHERE status='queued' so two reviewers approving
		// concurrently → one wins, the other gets RowsAffected=0
		// (which we surface as 409 Conflict).
		tag, err := tx.Exec(r.Context(), `
			UPDATE disposition_candidates
			   SET status = 'approved',
			       reviewer_id = $1,
			       decided_at = now(),
			       decided_reason = NULLIF($2, ''),
			       execute_after = $3
			 WHERE tenant_id = $4 AND id = $5 AND status = 'queued'
		`, userID, body.Reason, executeAt, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Either the row vanished (race with reject) or status moved.
			// Map to 409 — the reviewer's UI is stale.
			return vdmserr.Conflict("candidate already decided")
		}
		return h.emitDecision(r.Context(), tx, tenantID, id, docID, userID,
			"dms.disposition.approved.v1", "disposition.approved", body.Reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Either no matching candidate (404) or the doc is now held (409).
		// Disambiguate so the UI can show a useful error.
		writeProxyJSON(w, http.StatusConflict, map[string]any{
			"type":    "CONFLICT",
			"message": "candidate not approvable: not queued, document held, or document deleted",
		})
		return
	}
	if err != nil {
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"status":        "approved",
		"execute_after": executeAt,
	})
}

// reject is terminal — no executor pickup, no destruction.
func (h *DispositionHandler) reject(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := h.requireReviewer(w, r, true)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"type": "INVALID_ARGUMENT", "message": "invalid id"})
		return
	}
	body := decideBody{}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Reason == "" {
		// Reason is required for reject — without it the audit trail
		// has no signal about why a destruction was vetoed. Approve
		// can have an empty reason (the policy is the rationale); reject
		// is a deviation from the policy and needs explanation.
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{
			"type": "INVALID_ARGUMENT", "message": "reason required for reject",
		})
		return
	}

	var docID uuid.UUID
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			UPDATE disposition_candidates
			   SET status = 'rejected',
			       reviewer_id = $1,
			       decided_at = now(),
			       decided_reason = $2
			 WHERE tenant_id = $3 AND id = $4 AND status IN ('queued', 'approved')
			 RETURNING document_id
		`, userID, body.Reason, tenantID, id).Scan(&docID)
		if err != nil {
			return err
		}
		return h.emitDecision(r.Context(), tx, tenantID, id, docID, userID,
			"dms.disposition.rejected.v1", "disposition.rejected", body.Reason)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeProxyJSON(w, http.StatusConflict, map[string]any{
			"type": "CONFLICT", "message": "candidate not in a rejectable state",
		})
		return
	}
	if err != nil {
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "rejected"})
}

// emitDecision appends the audit event to the outbox in the same tx
// as the candidate mutation. Pattern matches the quarantine admin
// handler — destructive/governance actions never split DB and audit
// across transactions.
func (h *DispositionHandler) emitDecision(
	ctx context.Context, tx pgx.Tx,
	tenantID, candidateID, documentID, actorID uuid.UUID,
	eventType, action, reason string,
) error {
	payload, _ := json.Marshal(map[string]any{
		"actor_id":      actorID.String(),
		"tenant_id":     tenantID.String(),
		"action":        action,
		"resource_type": "document",
		"resource_id":   documentID.String(),
		"candidate_id":  candidateID.String(),
		"reason":        reason,
	})
	evt := database.NewOutboxEvent(tenantID, eventType, "document", documentID, payload)
	return h.outbox.Insert(ctx, tx, evt)
}
