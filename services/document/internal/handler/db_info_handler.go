// db_info_handler — platform-operator endpoint that reports the current
// database driver, version, and per-feature capability matrix
// (ADR 0094 §13.3). Read-only; no side effects.
//
// Future alt-driver support (MySQL / Oracle / SQL Server) will surface
// here automatically as the capability flags shift; the frontend
// rebuilds the same matrix from this response without UI changes.
package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/middleware"
)

// PlatformAdminChecker reports whether a user holds an active platform
// operator grant (platform_admins, ADR 0069).
type PlatformAdminChecker interface {
	IsPlatformAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// DBInfoHandler exposes GET /api/v1/admin/platform/db-info.
type DBInfoHandler struct {
	pool   *pgxpool.Pool
	admins PlatformAdminChecker
}

func NewDBInfoHandler(pool *pgxpool.Pool) *DBInfoHandler {
	h := &DBInfoHandler{pool: pool}
	if pool != nil {
		h.admins = poolPlatformAdmins{pool: pool}
	}
	return h
}

// Register mounts the route. The caller wraps it with SessionAuth; the
// handler itself enforces M-31: the server version and the capability
// matrix are platform internals, so the caller must be the tenant OWNER
// (RequireRole) AND hold an active platform_admins grant.
func (h *DBInfoHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/admin/platform/db-info",
		limitBody(MaxJSONBody, middleware.RequireRole("owner")(h.requirePlatformAdmin(h.get))))
}

// requirePlatformAdmin fails closed: no checker, a lookup error, or no
// active grant is 403.
func (h *DBInfoHandler) requirePlatformAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, err := auth.GetUserID(r.Context())
		if err != nil || uid == uuid.Nil || h.admins == nil {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"type": "FORBIDDEN", "message": "platform admin required"})
			return
		}
		ok, err := h.admins.IsPlatformAdmin(r.Context(), uid)
		if err != nil || !ok {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"type": "FORBIDDEN", "message": "platform admin required"})
			return
		}
		next(w, r)
	}
}

// poolPlatformAdmins reads the platform_admins allow-list (search migration
// 000003; platform-level, deliberately not tenant-scoped, no RLS).
type poolPlatformAdmins struct{ pool *pgxpool.Pool }

func (p poolPlatformAdmins) IsPlatformAdmin(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := p.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM platform_admins WHERE user_id = $1 AND revoked_at IS NULL
		)`, userID).Scan(&ok)
	return ok, err
}

type capabilityStatus string

const (
	capStatusNative         capabilityStatus = "native"
	capStatusDegraded       capabilityStatus = "degraded"
	capStatusUnsupported    capabilityStatus = "unsupported"
	capStatusNotImplemented capabilityStatus = "not_implemented"
)

type capability struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Status      capabilityStatus `json:"status"`
	Notes       string           `json:"notes,omitempty"`
}

type driverInfo struct {
	Name         string       `json:"name"`              // "postgres", "mysql", …
	DisplayName  string       `json:"display_name"`      // "PostgreSQL"
	Status       string       `json:"status"`            // "active" | "not_implemented"
	Version      string       `json:"version,omitempty"` // from SELECT version() on active driver
	Capabilities []capability `json:"capabilities"`
}

type dbInfoResponse struct {
	Active           driverInfo   `json:"active"`
	AlternateDrivers []driverInfo `json:"alternate_drivers"`
	ADR              string       `json:"adr"`
}

func (h *DBInfoHandler) get(w http.ResponseWriter, r *http.Request) {
	// Live-query the version from the active connection. If this fails
	// (e.g. pool is initialising) we still return the rest of the
	// response with version blank — the page degrades gracefully.
	pgVersion := "unavailable"
	if h.pool != nil {
		var v string
		if err := h.pool.QueryRow(r.Context(), "SELECT version()").Scan(&v); err == nil {
			pgVersion = v
		}
	}

	resp := dbInfoResponse{
		ADR: "0094",
		Active: driverInfo{
			Name:        "postgres",
			DisplayName: "PostgreSQL",
			Status:      "active",
			Version:     pgVersion,
			Capabilities: []capability{
				{Name: "rls", Description: "Row-level security (tenant isolation defense-in-depth)", Status: capStatusNative, Notes: "ENABLE ROW LEVEL SECURITY + NOBYPASSRLS app role"},
				{Name: "json", Description: "JSON columns with operator + indexing support", Status: capStatusNative, Notes: "JSONB + GIN"},
				{Name: "vector", Description: "Vector search for RAG / similarity", Status: capStatusNative, Notes: "pgvector extension"},
				{Name: "ltree", Description: "Folder path hierarchy", Status: capStatusNative, Notes: "LTREE extension"},
				{Name: "partial_index", Description: "Indexes with WHERE clause", Status: capStatusNative},
				{Name: "trigram_search", Description: "Fuzzy text search via trigrams", Status: capStatusNative, Notes: "pg_trgm"},
				{Name: "uuid_v7", Description: "Sortable time-ordered UUIDs", Status: capStatusNative, Notes: "gen_random_uuid()"},
			},
		},
		AlternateDrivers: []driverInfo{
			{
				Name:        "mysql",
				DisplayName: "MySQL 8",
				Status:      "not_implemented",
				Capabilities: []capability{
					{Name: "rls", Description: "Row-level security", Status: capStatusDegraded, Notes: "Phase 4: app-enforced + CI gate against raw SQL"},
					{Name: "json", Description: "JSON columns", Status: capStatusDegraded, Notes: "Native JSON type, but no native indexing on nested keys without virtual columns"},
					{Name: "vector", Description: "Vector search", Status: capStatusDegraded, Notes: "External Qdrant only — no pgvector equivalent"},
					{Name: "ltree", Description: "Folder hierarchy", Status: capStatusDegraded, Notes: "Recursive CTE emulation"},
					{Name: "partial_index", Description: "Partial indexes", Status: capStatusUnsupported, Notes: "MySQL does not support WHERE on CREATE INDEX"},
					{Name: "trigram_search", Description: "Fuzzy text", Status: capStatusUnsupported, Notes: "Falls back to OpenSearch"},
					{Name: "uuid_v7", Description: "UUIDv7", Status: capStatusDegraded, Notes: "App-side generation"},
				},
			},
			{
				Name:        "oracle",
				DisplayName: "Oracle 19c",
				Status:      "not_implemented",
				Capabilities: []capability{
					{Name: "rls", Description: "Row-level security", Status: capStatusNative, Notes: "Phase 2: VPD policies (DBMS_RLS.ADD_POLICY)"},
					{Name: "json", Description: "JSON columns", Status: capStatusDegraded, Notes: "Full support requires 21c+; 19c has partial"},
					{Name: "vector", Description: "Vector search", Status: capStatusDegraded, Notes: "External Qdrant only (19c). Oracle 23ai has native vector type."},
					{Name: "ltree", Description: "Folder hierarchy", Status: capStatusDegraded, Notes: "Recursive CTE emulation"},
					{Name: "partial_index", Description: "Partial indexes", Status: capStatusDegraded, Notes: "Function-based index emulation"},
					{Name: "trigram_search", Description: "Fuzzy text", Status: capStatusUnsupported, Notes: "Falls back to OpenSearch"},
					{Name: "uuid_v7", Description: "UUIDv7", Status: capStatusDegraded, Notes: "App-side generation"},
				},
			},
			{
				Name:        "sqlserver",
				DisplayName: "SQL Server 2019+",
				Status:      "not_implemented",
				Capabilities: []capability{
					{Name: "rls", Description: "Row-level security", Status: capStatusNative, Notes: "Phase 3: Security Policies (CREATE SECURITY POLICY)"},
					{Name: "json", Description: "JSON columns", Status: capStatusNative, Notes: "nvarchar(max) + JSON_VALUE / JSON_QUERY"},
					{Name: "vector", Description: "Vector search", Status: capStatusDegraded, Notes: "External Qdrant only"},
					{Name: "ltree", Description: "Folder hierarchy", Status: capStatusDegraded, Notes: "Recursive CTE emulation"},
					{Name: "partial_index", Description: "Partial indexes", Status: capStatusNative, Notes: "Filtered indexes"},
					{Name: "trigram_search", Description: "Fuzzy text", Status: capStatusUnsupported, Notes: "Falls back to OpenSearch"},
					{Name: "uuid_v7", Description: "UUIDv7", Status: capStatusDegraded, Notes: "App-side generation"},
				},
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
