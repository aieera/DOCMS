// Package handler exposes the public GraphQL endpoint.
//
// Two surfaces:
//   POST /api/v1/graphql    — main entry. Body is one of:
//       { "id": "<sha256>", "variables": {...}, "operationName": "..." }
//       { "query": "...", "variables": {...}, "operationName": "..." }   (dev only)
//   GET  /healthz           — liveness; returns 200 and the loaded
//                             persisted-query count.
package handler

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/exec"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/loader"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/persisted"
	"github.com/aieera/sedoc/services/graphql-gateway/internal/resolver"
)

// Config bundles the dependencies the handler needs at boot.
type Config struct {
	Executor   *exec.Executor
	Persisted  *persisted.Store
	Resolvers  *resolver.Resolver
	AllowDev   bool // if true, ?dev=1 + loopback can post inline queries
	Logger     zerolog.Logger
	NewLoaders func() *loader.Loaders
}

// New returns an http.Handler for GET /healthz + POST /api/v1/graphql.
// The /api/v1 prefix matches every other backend service in the repo
// so the Vite proxy + Kong gateway forward unchanged paths.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "ok",
			"persisted_count": cfg.Persisted.Size(),
		})
	})
	mux.HandleFunc("POST /api/v1/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Recover from any resolver / executor panic so the client
		// receives a structured 500 + the message instead of the
		// stdlib's stack-trace-as-text default. Logs the recovered
		// error so operators can root-cause from server logs.
		defer func() {
			if rec := recover(); rec != nil {
				cfg.Logger.Error().Interface("panic", rec).Msg("graphql handler panic")
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error":  "ServerError",
					"detail": stringifyPanic(rec),
				})
			}
		}()
		serve(w, r, cfg)
	})
	return mux
}

func stringifyPanic(rec any) string {
	switch v := rec.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return "panic"
	}
}

type request struct {
	ID            string         `json:"id,omitempty"`
	Query         string         `json:"query,omitempty"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	// APQ-style envelope: { extensions: { persistedQuery: { version: 1,
	// sha256Hash: "..." } } }. Lifted from the standard Apollo /
	// urql persisted-query exchange so we can plug those clients in
	// without a custom HTTP exchange. When both `id` and
	// extensions.persistedQuery.sha256Hash are present, extensions
	// wins (clients that opt into the APQ shape never set top-level id).
	Extensions struct {
		PersistedQuery struct {
			Version    int    `json:"version,omitempty"`
			SHA256Hash string `json:"sha256Hash,omitempty"`
		} `json:"persistedQuery,omitempty"`
	} `json:"extensions,omitempty"`
}

func serve(w http.ResponseWriter, r *http.Request, cfg Config) {
	// SEC-2: identity comes from SessionAuth-populated ctx (trusted DB
	// session). Kong strips client-supplied X-Auth-Tenant-ID / X-Auth-User-ID
	// on inbound, so the header read this used to do was always empty in
	// prod — the handler never actually authenticated anyone. Wrap order
	// in cmd/server/main.go: RequireGatewaySignature → SessionAuth →
	// rate-limit → this handler.
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	role := auth.RoleString(r)
	if tenantID == "" || userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant + user identity required"})
		return
	}

	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// Resolve the operation document. Either `id` (our shape) or
	// `extensions.persistedQuery.sha256Hash` (Apollo / urql APQ
	// shape) selects a manifest entry; the two are equivalent and
	// the APQ value wins when both are sent (urql opts into that
	// shape exclusively).
	hash := req.ID
	if h := req.Extensions.PersistedQuery.SHA256Hash; h != "" {
		hash = h
	}
	var query string
	switch {
	case hash != "":
		entry, err := cfg.Persisted.Lookup(hash)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "PersistedQueryNotFound",
				"hash":  hash,
			})
			return
		}
		query = entry.Doc
	case req.Query != "" && cfg.AllowDev && devEscapeOK(r):
		query = req.Query
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "PersistedQueryRequired",
			"hint":  "submit { id } from your build manifest, or set ?dev=1 from loopback in a dev build",
		})
		return
	}

	// Per-request loader bundle; cache lifetime = handler return.
	ctx := loader.WithLoaders(r.Context(), cfg.NewLoaders())
	ctx = resolver.WithIdentity(ctx, tenantID, userID, role)

	result := cfg.Executor.Execute(ctx, query, req.Variables, req.OperationName)
	writeJSON(w, http.StatusOK, result)
}

// devEscapeOK lets ?dev=1 inline queries through *only* when the
// request comes from loopback. Production binaries pass
// AllowDev=false; this never fires there even if a tenant added
// the query parameter.
func devEscapeOK(r *http.Request) bool {
	if r.URL.Query().Get("dev") != "1" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
