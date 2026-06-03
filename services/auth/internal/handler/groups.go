// Groups admin HTTP handlers — list/create/rename/delete + member
// add/remove. Routes mount at /api/v1/admin/groups/* and are wrapped
// in AuthMiddleware + RequireRole("admin", "owner") by the router.
//
// The SCIM surface at /scim/v2/{slug}/Groups already exposes the
// same tables, but SCIM uses per-tenant bearer tokens and the admin
// UI uses session cookies. Rather than bridge the two auth models we
// keep the admin surface as a thin REST façade over the groups /
// group_members tables.
//
// Queries run with WithTenantTx so RLS (`app.current_tenant` GUC) is
// enforced on every read and write.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/validation"
)

// GroupsHandler wires /api/v1/admin/groups/* onto the auth service.
type GroupsHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
	// env is the runtime environment string from pkg/config ("dev",
	// "staging", "prod"). The name-min-length validator changes
	// behavior in prod (reject) vs. non-prod (warn-only).
	env string
}

// NewGroupsHandler constructs the handler.
func NewGroupsHandler(pool *pgxpool.Pool, log zerolog.Logger, env string) *GroupsHandler {
	return &GroupsHandler{pool: pool, log: log, env: env}
}

// Mount attaches routes to the chi router. Caller wraps the group
// with AuthMiddleware + RequireRole.
func (g *GroupsHandler) Mount(r chi.Router) {
	r.Get("/", g.list)
	r.Post("/", g.create)
	r.Get("/{id}", g.get)
	r.Patch("/{id}", g.update)
	r.Delete("/{id}", g.delete)
	r.Post("/{id}/members", g.addMember)
	r.Delete("/{id}/members/{userId}", g.removeMember)
}

// ---- DTOs -----------------------------------------------------------------

type groupDTO struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedBy   string    `json:"created_by,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	MemberCount int       `json:"member_count"`
}

type memberDTO struct {
	UserID      string `json:"user_id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	AddedAt     time.Time `json:"added_at"`
}

type groupDetailDTO struct {
	groupDTO
	Members []memberDTO `json:"members"`
}

// ---- Handlers -------------------------------------------------------------

func (g *GroupsHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	out := []groupDTO{}
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT g.id::text, g.name, COALESCE(g.description,''),
			       COALESCE(g.created_by::text,''), g.created_at, g.updated_at,
			       (SELECT COUNT(*) FROM group_members gm WHERE gm.tenant_id = g.tenant_id AND gm.group_id = g.id)
			  FROM groups g
			 WHERE g.tenant_id = $1 AND g.deleted_at IS NULL
			 ORDER BY g.name`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d groupDTO
			if err := rows.Scan(&d.ID, &d.Name, &d.Description, &d.CreatedBy,
				&d.CreatedAt, &d.UpdatedAt, &d.MemberCount); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	g.writeJSON(w, http.StatusOK, out)
}

func (g *GroupsHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var detail groupDetailDTO
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			SELECT g.id::text, g.name, COALESCE(g.description,''),
			       COALESCE(g.created_by::text,''), g.created_at, g.updated_at
			  FROM groups g
			 WHERE g.tenant_id = $1 AND g.id = $2 AND g.deleted_at IS NULL`,
			tenantID, id,
		).Scan(&detail.ID, &detail.Name, &detail.Description, &detail.CreatedBy,
			&detail.CreatedAt, &detail.UpdatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("group not found")
			}
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT u.id::text, u.email, COALESCE(u.display_name,''), gm.added_at
			  FROM group_members gm
			  JOIN users u ON u.tenant_id = gm.tenant_id AND u.id = gm.user_id
			 WHERE gm.tenant_id = $1 AND gm.group_id = $2
			 ORDER BY u.display_name, u.email`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		detail.Members = []memberDTO{} // never nil — JSON serializes as [] not null
		for rows.Next() {
			var m memberDTO
			if err := rows.Scan(&m.UserID, &m.Email, &m.DisplayName, &m.AddedAt); err != nil {
				return err
			}
			detail.Members = append(detail.Members, m)
		}
		detail.MemberCount = len(detail.Members)
		return rows.Err()
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	g.writeJSON(w, http.StatusOK, detail)
}

type createGroupBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (g *GroupsHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	var body createGroupBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		g.writeErr(w, r, vdmserr.Validation("name", "required"))
		return
	}
	// Min-length check. In prod it's a hard 400; in dev/staging it
	// only logs a warning so seeded test data still loads. Stops the
	// "g" / "uu" / "x" class of staging-leaked test rows.
	if err := validation.EntityName(g.env, body.Name); err != nil {
		g.writeErr(w, r, vdmserr.Validation("name", "must be at least 2 characters"))
		return
	}
	if validation.EntityNameTooShort(body.Name) {
		g.log.Warn().Str("name", body.Name).Str("tenant", tenantID.String()).Msg("group name shorter than 2 chars (allowed in non-prod)")
	}
	id := uuid.New()
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO groups (tenant_id, id, name, description, created_by)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5)`,
			tenantID, id, body.Name, body.Description, userID,
		)
		return err
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	g.writeJSON(w, http.StatusCreated, map[string]any{
		"id":          id.String(),
		"name":        body.Name,
		"description": body.Description,
		"member_count": 0,
	})
}

type updateGroupBody struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

func (g *GroupsHandler) update(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body updateGroupBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		g.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			UPDATE groups
			   SET name = COALESCE($1, name),
			       description = COALESCE($2, description)
			 WHERE tenant_id = $3 AND id = $4 AND deleted_at IS NULL`,
			body.Name, body.Description, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("group not found")
		}
		return nil
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *GroupsHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE groups SET deleted_at = now()
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
			tenantID, id)
		return err
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type addMemberBody struct {
	UserID string `json:"user_id"`
}

func (g *GroupsHandler) addMember(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	groupID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body addMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		g.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("user_id", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO group_members (tenant_id, group_id, user_id, added_by)
			VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			tenantID, groupID, userID, actorID)
		return err
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *GroupsHandler) removeMember(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	groupID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userId"))
	if err != nil {
		g.writeErr(w, r, vdmserr.Validation("userId", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), g.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			DELETE FROM group_members
			 WHERE tenant_id = $1 AND group_id = $2 AND user_id = $3`,
			tenantID, groupID, userID)
		return err
	})
	if err != nil {
		g.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers --------------------------------------------------------------

func (g *GroupsHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (g *GroupsHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	corr := r.Header.Get("X-Correlation-ID")
	httpErr := vdmserr.ToHTTPError(err, corr)
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	g.writeJSON(w, httpErr.Code, httpErr)
}

var _ = context.Background // keep context import available
