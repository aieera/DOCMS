// clauses_handler — clause library CRUD + search (ADR 0104 §18 F11).
//
// Five endpoints:
//   GET    /api/v1/clauses           — list, with ?q=, ?jurisdiction=, ?tag=, ?limit=
//   GET    /api/v1/clauses/{id}      — single
//   POST   /api/v1/clauses           — create (admin/owner only at mount site)
//   PATCH  /api/v1/clauses/{id}      — update + auto-bump version
//   DELETE /api/v1/clauses/{id}      — soft delete
//
// All routes use SessionAuth at the mount site (sets tenant + user
// on ctx). RLS handles tenant isolation; admin/owner check is at the
// mount layer for the mutating routes.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
)

type ClausesHandler struct{ pool *pgxpool.Pool }

func NewClausesHandler(pool *pgxpool.Pool) *ClausesHandler {
	return &ClausesHandler{pool: pool}
}

func (h *ClausesHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clauses", h.list)
	mux.HandleFunc("GET /api/v1/clauses/{id}", h.get)
	mux.HandleFunc("POST /api/v1/clauses", h.create)
	mux.HandleFunc("PATCH /api/v1/clauses/{id}", h.patch)
	mux.HandleFunc("DELETE /api/v1/clauses/{id}", h.softDelete)
}

// ---- types --------------------------------------------------------

type clauseRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	BodyText     string    `json:"body_text"`
	Jurisdiction string    `json:"jurisdiction"`
	Tags         []string  `json:"tags"`
	Version      int       `json:"version"`
	ApprovedBy   *string   `json:"approved_by,omitempty"`
	ApprovedAt   *string   `json:"approved_at,omitempty"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SearchRank   *float32  `json:"search_rank,omitempty"`
}

type listResp struct {
	Clauses []clauseRow `json:"clauses"`
	Total   int         `json:"total"`
}

// ---- list ---------------------------------------------------------

func (h *ClausesHandler) list(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	q := r.URL.Query().Get("q")
	jurisdiction := r.URL.Query().Get("jurisdiction")
	tag := r.URL.Query().Get("tag")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var out listResp
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		// Two queries: one rank-ordered slice + a total count for the
		// header. The query plan uses idx_clauses_search when q is
		// present and idx_clauses_tenant_active otherwise.
		var (
			rows pgx.Rows
			e    error
		)
		if q != "" {
			rows, e = tx.Query(r.Context(), `
				SELECT id, name, body_text, jurisdiction, tags, version,
				       approved_by, approved_at, created_by, created_at, updated_at,
				       ts_rank(search_tsv, plainto_tsquery('english', $2)) AS rank
				FROM clauses
				WHERE tenant_id = $1
				  AND deleted_at IS NULL
				  AND search_tsv @@ plainto_tsquery('english', $2)
				  AND ($3 = '' OR jurisdiction = $3)
				  AND ($4 = '' OR $4 = ANY(tags))
				ORDER BY rank DESC, updated_at DESC
				LIMIT $5`,
				tid, q, jurisdiction, tag, limit)
		} else {
			rows, e = tx.Query(r.Context(), `
				SELECT id, name, body_text, jurisdiction, tags, version,
				       approved_by, approved_at, created_by, created_at, updated_at,
				       NULL::real AS rank
				FROM clauses
				WHERE tenant_id = $1
				  AND deleted_at IS NULL
				  AND ($2 = '' OR jurisdiction = $2)
				  AND ($3 = '' OR $3 = ANY(tags))
				ORDER BY updated_at DESC
				LIMIT $4`,
				tid, jurisdiction, tag, limit)
		}
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanClauseWithRank(rows)
			if err != nil {
				return err
			}
			out.Clauses = append(out.Clauses, c)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Cheap total — same filter, no LIMIT.
		return tx.QueryRow(r.Context(), `
			SELECT COUNT(*) FROM clauses
			WHERE tenant_id = $1
			  AND deleted_at IS NULL
			  AND ($2 = '' OR jurisdiction = $2)
			  AND ($3 = '' OR $3 = ANY(tags))
			  AND ($4 = '' OR search_tsv @@ plainto_tsquery('english', $4))`,
			tid, jurisdiction, tag, q,
		).Scan(&out.Total)
	})
	if err != nil {
		slog.Error("clauses list failed", "tenant", tid, "err", err.Error())
		writeJSON(w, 500, map[string]string{"error": "list failed"})
		return
	}
	if out.Clauses == nil {
		out.Clauses = []clauseRow{}
	}
	writeJSON(w, 200, out)
}

// ---- get ----------------------------------------------------------

func (h *ClausesHandler) get(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil || tid == uuid.Nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "id not a uuid"})
		return
	}
	var c clauseRow
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		c, err = fetchClause(r.Context(), tx, tid, id)
		return err
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "fetch failed"})
		return
	}
	writeJSON(w, 200, c)
}

// ---- create -------------------------------------------------------

type createClauseReq struct {
	Name         string   `json:"name"`
	BodyText     string   `json:"body_text"`
	Jurisdiction string   `json:"jurisdiction"`
	Tags         []string `json:"tags"`
}

func (h *ClausesHandler) create(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	uid, uerr := tenantOwnerOrFail(r, w)
	if uerr != nil {
		return
	}
	var in createClauseReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}
	if in.Name == "" || in.BodyText == "" {
		writeJSON(w, 400, map[string]string{"error": "name and body_text required"})
		return
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}
	var newID uuid.UUID
	err := database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			INSERT INTO clauses
				(tenant_id, name, body_text, jurisdiction, tags, created_by)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			tid, in.Name, in.BodyText, in.Jurisdiction, in.Tags, uid,
		).Scan(&newID)
	})
	if err != nil {
		slog.Error("clauses create failed", "tenant", tid, "err", err.Error())
		writeJSON(w, 500, map[string]string{"error": "create failed"})
		return
	}
	writeJSON(w, 201, map[string]string{"id": newID.String()})
}

// ---- patch (auto-bump version) -----------------------------------

type patchClauseReq struct {
	Name         *string   `json:"name,omitempty"`
	BodyText     *string   `json:"body_text,omitempty"`
	Jurisdiction *string   `json:"jurisdiction,omitempty"`
	Tags         *[]string `json:"tags,omitempty"`
}

func (h *ClausesHandler) patch(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	if _, uerr := tenantOwnerOrFail(r, w); uerr != nil {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "id not a uuid"})
		return
	}
	var in patchClauseReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid json"})
		return
	}

	// Update — COALESCE keeps unspecified fields. version auto-bumps
	// when any text/tags column changes. Approval state changes ONLY
	// via POST/DELETE /api/v1/clauses/{id}/approve (admin/owner-gated,
	// see clause_matches_handler.go); PATCH deliberately cannot touch
	// approved_by/approved_at — tenantOwnerOrFail here checks
	// authentication only, not role, so honoring an "approved" body
	// field would let any tenant member self-approve a clause.
	var out clauseRow
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `
			UPDATE clauses
			SET name         = COALESCE($3, name),
			    body_text    = COALESCE($4, body_text),
			    jurisdiction = COALESCE($5, jurisdiction),
			    tags         = COALESCE($6, tags),
			    version      = CASE WHEN $3 IS NOT NULL OR $4 IS NOT NULL
			                          OR $5 IS NOT NULL OR $6 IS NOT NULL
			                        THEN version + 1 ELSE version END,
			    updated_at   = NOW()
			WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tid, id, in.Name, in.BodyText, in.Jurisdiction, in.Tags,
		)
		if e != nil {
			return e
		}
		out, e = fetchClause(r.Context(), tx, tid, id)
		return e
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "update failed", "detail": err.Error()})
		return
	}
	writeJSON(w, 200, out)
}

// ---- soft delete --------------------------------------------------

func (h *ClausesHandler) softDelete(w http.ResponseWriter, r *http.Request) {
	tid, terr := auth.GetTenantID(r.Context())
	if terr != nil {
		writeJSON(w, 401, map[string]string{"error": "no tenant"})
		return
	}
	if _, err := tenantOwnerOrFail(r, w); err != nil {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": "id not a uuid"})
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tid, func(tx pgx.Tx) error {
		tag, e := tx.Exec(r.Context(),
			`UPDATE clauses SET deleted_at = NOW(), updated_at = NOW()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tid, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, 404, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, 500, map[string]string{"error": "delete failed"})
		return
	}
	w.WriteHeader(204)
}

// ---- helpers ------------------------------------------------------

func fetchClause(ctx context.Context, tx pgx.Tx, tid, id uuid.UUID) (clauseRow, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, name, body_text, jurisdiction, tags, version,
		       approved_by, approved_at, created_by, created_at, updated_at,
		       NULL::real AS rank
		FROM clauses
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`, tid, id)
	return scanClauseWithRank(row)
}

// rowScanner is satisfied by both pgx.Row and pgx.Rows so the same
// helper handles single-row and looped-row paths.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanClauseWithRank(r rowScanner) (clauseRow, error) {
	var (
		c           clauseRow
		approvedBy  *uuid.UUID
		approvedAt  *time.Time
		createdBy   uuid.UUID
		rank        *float32
		idUUID      uuid.UUID
		jurisd      string
	)
	if err := r.Scan(&idUUID, &c.Name, &c.BodyText, &jurisd, &c.Tags, &c.Version,
		&approvedBy, &approvedAt, &createdBy, &c.CreatedAt, &c.UpdatedAt, &rank); err != nil {
		return c, err
	}
	c.ID = idUUID.String()
	c.Jurisdiction = jurisd
	c.CreatedBy = createdBy.String()
	if approvedBy != nil {
		s := approvedBy.String()
		c.ApprovedBy = &s
	}
	if approvedAt != nil {
		s := approvedAt.UTC().Format(time.RFC3339)
		c.ApprovedAt = &s
	}
	if rank != nil {
		c.SearchRank = rank
	}
	if c.Tags == nil {
		c.Tags = []string{}
	}
	return c, nil
}
