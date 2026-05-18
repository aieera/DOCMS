// HTTP transport for the MCP server. Two endpoint families:
//
//   POST /api/v1/mcp                       — single JSON-RPC call,
//                                            response in body. Used
//                                            by curl + the admin
//                                            "Test MCP" button.
//   GET  /api/v1/mcp/sse                   — open SSE stream. Server
//                                            emits an `endpoint` event
//                                            telling the client where
//                                            to POST, then pushes
//                                            `message` events as the
//                                            dispatcher produces them.
//   POST /api/v1/mcp/sse?session_id=<uuid> — client posts JSON-RPC
//                                            frames here; server
//                                            returns 202 Accepted and
//                                            queues the response onto
//                                            the matching SSE outbox.
//
// All routes are wrapped by middleware.APIKeyAuth with required scope
// `mcp:read` at the route boundary; per-tool scopes (read vs write)
// are checked inside the dispatcher.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/mcp-server/internal/mcp"
)

type Handler struct {
	srv      *mcp.Server
	log      zerolog.Logger
	sessions *sessionRegistry
}

func New(srv *mcp.Server, log zerolog.Logger) *Handler {
	return &Handler{srv: srv, log: log, sessions: newSessionRegistry()}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/mcp", h.singleCall)
	mux.HandleFunc("GET /api/v1/mcp/sse", h.sseStream)
	mux.HandleFunc("POST /api/v1/mcp/sse", h.sseInbound)
}

// singleCall: one request, one response. JSON in, JSON out. Used by
// curl-equivalent clients and the in-app admin "Test MCP" button.
func (h *Handler) singleCall(w http.ResponseWriter, r *http.Request) {
	var req mcp.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	resp := h.srv.Dispatch(r.Context(), req)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// sseStream: opens a session and holds the connection open, pushing
// `event: message` frames as the dispatcher produces responses for
// this session. First frame is always an `event: endpoint` telling
// the client where to POST requests — Claude Desktop / Cursor read
// this to discover the POST URL.
func (h *Handler) sseStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	sess := h.sessions.create()
	defer h.sessions.close(sess.id)

	// Per MCP HTTP+SSE transport: first event tells the client the
	// POST URL. We include session_id as a query param so concurrent
	// MCP clients (Claude + Cursor on the same machine) don't collide.
	endpoint := "/api/v1/mcp/sse?session_id=" + sess.id
	if _, err := fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpoint); err != nil {
		return
	}
	flusher.Flush()

	// Pump outbox → SSE. Heartbeat every 15s as an SSE comment so any
	// idle-killing middlebox (Cloudflare, nginx) keeps the stream open.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprintf(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case resp, ok := <-sess.outbox:
			if !ok {
				return
			}
			raw, _ := json.Marshal(resp)
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// sseInbound: client POSTs a JSON-RPC frame with ?session_id=<uuid>;
// we dispatch and push the response onto the session's outbox so it
// rides back to the client via the open SSE GET. Returns 202 Accepted
// with empty body — the actual response travels through the SSE pipe.
func (h *Handler) sseInbound(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, `{"error":"session_id query param required"}`, http.StatusBadRequest)
		return
	}
	sess, ok := h.sessions.get(sessionID)
	if !ok {
		http.Error(w, `{"error":"unknown session_id"}`, http.StatusNotFound)
		return
	}
	var req mcp.Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}
	resp := h.srv.Dispatch(r.Context(), req)
	// Non-blocking enqueue. Outbox buffer is 16; dropping is acceptable
	// only if the client has stopped reading (in which case the SSE
	// loop is about to exit anyway). Use select+default so a slow
	// client can never block the dispatcher.
	select {
	case sess.outbox <- resp:
	default:
		h.log.Warn().Str("session_id", sessionID).Str("method", req.Method).Msg("mcp outbox full; response dropped")
	}
	w.WriteHeader(http.StatusAccepted)
}
