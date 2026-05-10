// Package handler exposes the public GraphQL endpoint.
//
// Two surfaces:
//   POST /graphql           — main entry. Body is one of:
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

	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/exec"
	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/loader"
	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/persisted"
	"github.com/vaultdms/vaultdms/services/graphql-gateway/internal/resolver"
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

// New returns an http.Handler for GET /healthz + POST /graphql.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":          "ok",
			"persisted_count": cfg.Persisted.Size(),
		})
	})
	mux.HandleFunc("POST /graphql", func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, cfg)
	})
	return mux
}

type request struct {
	ID            string         `json:"id,omitempty"`
	Query         string         `json:"query,omitempty"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
}

func serve(w http.ResponseWriter, r *http.Request, cfg Config) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-Auth-User-ID")
	if tenantID == "" || userID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "tenant + user identity required"})
		return
	}

	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	// Resolve the operation document. Persisted-query path takes
	// precedence — if the client sent an id, we look it up; an
	// inline query alongside an id is ignored to keep the contract
	// strict.
	var query string
	switch {
	case req.ID != "":
		entry, err := cfg.Persisted.Lookup(req.ID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "PersistedQueryNotFound",
				"hash":  req.ID,
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
	ctx = resolver.WithIdentity(ctx, tenantID, userID)

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
