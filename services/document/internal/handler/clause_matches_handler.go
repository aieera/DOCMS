// clause_matches_handler — ADR 0104 Phases 2/4 + approval.
//
//	GET    /api/v1/documents/{id}/clause-matches  — detection results for the panel
//	GET    /api/v1/clauses/{id}/variations        — usage variants (Phase 4)
//	POST   /api/v1/clauses/{id}/approve           — approve (admin/owner)
//	DELETE /api/v1/clauses/{id}/approve           — revoke (admin/owner)
//
// Detection rows are written by the intelligence detect_clauses task.
// Variations group by md5(lower + collapsed whitespace) of matched_text
// — keep in sync with _normalize_text in intelligence clause_match.py.
package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

type ClauseMatchesHandler struct{ pool *pgxpool.Pool }

func NewClauseMatchesHandler(pool *pgxpool.Pool) *ClauseMatchesHandler {
	return &ClauseMatchesHandler{pool: pool}
}

func (h *ClauseMatchesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/documents/{id}/clause-matches", h.listForDocument)
	mux.HandleFunc("GET /api/v1/clauses/{id}/variations", h.variations)
	mux.HandleFunc("POST /api/v1/clauses/{id}/approve", h.approve)
	mux.HandleFunc("DELETE /api/v1/clauses/{id}/approve", h.revoke)
}

type clauseMatchRow struct {
	ClauseID     string    `json:"clause_id"`
	ClauseName   string    `json:"clause_name"`
	Jurisdiction string    `json:"jurisdiction"`
	Approved     bool      `json:"approved"`
	Similarity   float32   `json:"similarity"`
	ChunkIndex   int       `json:"chunk_index"`
	MatchedText  string    `json:"matched_text"`
	DetectedAt   time.Time `json:"detected_at"`
}

func (h *ClauseMatchesHandler) listForDocument(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	docID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid document id"})
		return
	}
	matches := []clauseMatchRow{}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// Version scoping: the detection task delete+reinserts only its
		// OWN version's rows, so rows from prior versions persist by
		// design. The panel must show the current head's matches only —
		// join documents and filter on current_version_id (mirrors the
		// version-scoped Qdrant filter on the Python detection side).
		rows, e := tx.Query(r.Context(), `
			SELECT m.clause_id, c.name, c.jurisdiction,
			       (c.approved_at IS NOT NULL) AS approved,
			       m.similarity, m.chunk_index, m.matched_text, m.detected_at
			  FROM clause_matches m
			  JOIN clauses c   ON c.tenant_id = m.tenant_id AND c.id = m.clause_id
			  JOIN documents d ON d.tenant_id = m.tenant_id AND d.id = m.document_id
			 WHERE m.tenant_id = $1 AND m.document_id = $2
			   AND m.version_id = d.current_version_id
			   AND c.deleted_at IS NULL
			 ORDER BY m.similarity DESC`,
			tid, docID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var m clauseMatchRow
			var cid uuid.UUID
			if e := rows.Scan(&cid, &m.ClauseName, &m.Jurisdiction, &m.Approved,
				&m.Similarity, &m.ChunkIndex, &m.MatchedText, &m.DetectedAt); e != nil {
				return e
			}
			m.ClauseID = cid.String()
			matches = append(matches, m)
		}
		return rows.Err()
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{"matches": matches})
}

type variationRow struct {
	NormalizedHash string   `json:"normalized_hash"`
	Occurrences    int      `json:"occurrences"`
	SampleText     string   `json:"sample_text"`
	MinSimilarity  float32  `json:"min_similarity"`
	MaxSimilarity  float32  `json:"max_similarity"`
	DocumentIDs    []string `json:"document_ids"`
}

func (h *ClauseMatchesHandler) variations(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	variations := []variationRow{}
	var totalDocs int
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// Normalization MUST mirror intelligence _normalize_text
		// byte-for-byte: lowercase → collapse whitespace runs to a
		// single space → trim. Collapse BEFORE btrim: btrim only
		// strips literal spaces, so edge tabs/newlines must first be
		// collapsed into spaces (review finding on Task 2).
		rows, e := tx.Query(r.Context(), `
			SELECT md5(btrim(regexp_replace(lower(matched_text), '\s+', ' ', 'g'))) AS h,
			       count(*)                                       AS occurrences,
			       min(matched_text)                              AS sample_text,
			       min(similarity)                                AS min_sim,
			       max(similarity)                                AS max_sim,
			       array_agg(DISTINCT document_id::text)          AS doc_ids
			  FROM clause_matches
			 WHERE tenant_id = $1 AND clause_id = $2
			 GROUP BY 1
			 ORDER BY occurrences DESC, max_sim DESC
			 LIMIT 100`,
			tid, clauseID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v variationRow
			if e := rows.Scan(&v.NormalizedHash, &v.Occurrences, &v.SampleText,
				&v.MinSimilarity, &v.MaxSimilarity, &v.DocumentIDs); e != nil {
				return e
			}
			variations = append(variations, v)
		}
		if e := rows.Err(); e != nil {
			return e
		}
		return tx.QueryRow(r.Context(), `
			SELECT count(DISTINCT document_id) FROM clause_matches
			 WHERE tenant_id = $1 AND clause_id = $2`,
			tid, clauseID).Scan(&totalDocs)
	})
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{"variations": variations, "total_documents": totalDocs})
}

func (h *ClauseMatchesHandler) approve(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	// Same role gate as the package's other mutating endpoints
	// (compliance_handler.requireRole) so the 403 body is uniform.
	if !requireRole(w, r, "admin", "owner") {
		return
	}
	u, _ := auth.User(r.Context())
	uid := u.ID
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	var approvedAt time.Time
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			UPDATE clauses
			   SET approved_by = $3, approved_at = now(), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
			 RETURNING approved_at`,
			tid, clauseID, uid).Scan(&approvedAt)
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, 404, map[string]string{"error": "clause not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"approved_by": uid.String(),
		// RFC3339 string, matching scanClauseWithRank's formatting of
		// the same column in the clause CRUD responses.
		"approved_at": approvedAt.UTC().Format(time.RFC3339),
	})
}

func (h *ClauseMatchesHandler) revoke(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	if !requireRole(w, r, "admin", "owner") {
		return
	}
	clauseID, perr := uuid.Parse(r.PathValue("id"))
	if perr != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid clause id"})
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(), `
			UPDATE clauses
			   SET approved_by = NULL, approved_at = NULL, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tid, clauseID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			writeJSON(w, 404, map[string]string{"error": "clause not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "revoked"})
}
