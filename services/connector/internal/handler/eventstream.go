// Package handler — eventstream routes (ADR 0077).
//
// Mounts:
//   POST   /api/v1/admin/event-stream/tokens          — issue a token (returns plaintext once)
//   GET    /api/v1/admin/event-stream/tokens          — list (no plaintext)
//   DELETE /api/v1/admin/event-stream/tokens/{id}     — revoke
//   GET    /api/v1/admin/event-stream/tail            — SSE live tail (60s window)
//   GET    /api/v1/events                             — polling endpoint (bearer auth)
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/eventstream"
)

// EventStreamHandler wires ADR-0077 HTTP routes onto the connector
// service. It deliberately doesn't share the Handler struct above
// because the polling endpoint needs a different auth model (Bearer
// token instead of gateway signature) and pulling that into the
// generic handler.go would muddy the existing surface.
type EventStreamHandler struct {
	svc *eventstream.Service
}

// NewEventStreamHandler constructs a handler.
func NewEventStreamHandler(svc *eventstream.Service) *EventStreamHandler {
	return &EventStreamHandler{svc: svc}
}

// Register mounts the routes on the supplied mux. The admin routes
// run behind the gateway-signature middleware (added in main.go); the
// /api/v1/events polling route is mounted on a separate path that the
// gateway forwards without the signature check (Bearer auth instead).
func (h *EventStreamHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/event-stream/tokens", h.issueToken)
	mux.HandleFunc("GET /api/v1/admin/event-stream/tokens", h.listTokens)
	mux.HandleFunc("DELETE /api/v1/admin/event-stream/tokens/{id}", h.revokeToken)
	mux.HandleFunc("GET /api/v1/admin/event-stream/tail", h.tail)
	mux.HandleFunc("GET /api/v1/events", h.pollEvents)
}

// ---- Token CRUD -----------------------------------------------------------

type issueTokenBody struct {
	Label string `json:"label"`
}

func (h *EventStreamHandler) issueToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	actorID := auth.UserIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	var body issueTokenBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Label == "" {
		writeError(w, http.StatusBadRequest, "label required")
		return
	}
	tok, err := h.svc.IssueToken(r.Context(), tenantID, actorID, body.Label)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, tok)
}

func (h *EventStreamHandler) listTokens(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	list, err := h.svc.ListTokens(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *EventStreamHandler) revokeToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	if err := h.svc.RevokeToken(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Live tail (SSE) ------------------------------------------------------

func (h *EventStreamHandler) tail(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 60-second window — long enough to be useful, short enough that an
	// abandoned browser tab doesn't pin a JetStream consumer forever.
	err := h.svc.TailEvents(r.Context(), tenantID, 60*time.Second, func(ev *eventstream.Event) error {
		b, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", string(b)); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if err != nil {
		// Best-effort: write a terminal event so the EventSource on the FE
		// gets a hint to stop reconnecting.
		_, _ = fmt.Fprintf(w, "event: end\ndata: %q\n\n", err.Error())
		flusher.Flush()
	}
}

// ---- Polling --------------------------------------------------------------

func (h *EventStreamHandler) pollEvents(w http.ResponseWriter, r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == "" {
		writeError(w, http.StatusUnauthorized, "bearer token required")
		return
	}
	tenantID, err := h.svc.LookupTokenTenant(r.Context(), bearer)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "invalid or revoked token")
		return
	}

	q := r.URL.Query()
	var since time.Time
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = t
	}
	cursor := q.Get("cursor")
	limit := 100
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	events, next, hasMore, err := h.svc.ListEvents(r.Context(), tenantID, since, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":      events,
		"next_cursor": next,
		"has_more":    hasMore,
	})
}