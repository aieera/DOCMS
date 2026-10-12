// Session registry for the MCP HTTP+SSE transport (protocol 2024-11-05).
//
// The spec requires a two-endpoint dance:
//
//   1. Client opens GET /sse. Server creates a session, emits an
//      `endpoint` event whose data is the POST URL the client should
//      send requests to (with the session_id embedded). Server then
//      holds the GET connection open and pushes JSON-RPC frames as
//      `event: message` whenever the dispatcher has a response or a
//      server-initiated notification.
//
//   2. Client POSTs each JSON-RPC frame to the URL it learned in (1).
//      Server returns 202 Accepted IMMEDIATELY (the response body is
//      empty); the actual JSON-RPC response is queued onto the SSE
//      channel for that session and rides back through (1).
//
// Without this two-leg flow, Claude Code / Cursor open the SSE GET,
// see no `endpoint` event, and time out. The earlier hand-rolled
// scaffold returned the response in the POST body which works for
// curl but not for real MCP clients.
package handler

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/mcp-server/internal/mcp"
)

// session is one open SSE connection. The outbox channel is what the
// GET handler ranges over to push events; the POST handler writes to
// it (via send) after dispatching a request.
//
// A session is bound to the tenant + user of the API key that opened it
// (R-6): only that identity may POST frames into it, so another key that
// learns the session_id cannot inject responses into this stream.
type session struct {
	id       string
	tenantID uuid.UUID
	userID   uuid.UUID
	outbox   chan mcp.Response

	// mu guards closed and the close of outbox, so a POST racing the GET
	// leg's shutdown never sends on a closed channel.
	mu     sync.Mutex
	closed bool
}

// ownedBy reports whether ctx carries the identity that opened the session.
func (s *session) ownedBy(ctx context.Context) bool {
	u, err := auth.User(ctx)
	if err != nil || u.TenantID == uuid.Nil {
		return false
	}
	return u.TenantID == s.tenantID && u.ID == s.userID
}

// send enqueues resp without blocking. It returns false when the session
// is already closed or the outbox is full.
func (s *session) send(resp mcp.Response) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.outbox <- resp:
		return true
	default:
		return false
	}
}

func (s *session) shut() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.outbox)
	}
}

type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[string]*session)}
}

// create returns a fresh session with a buffered outbox, bound to the
// identity on ctx (stamped by APIKeyAuth). Buffer size 16 — enough to
// handle a burst (initialize + tools/list + a few tools/call) without
// blocking the dispatcher.
func (r *sessionRegistry) create(ctx context.Context) *session {
	id := uuid.New().String()
	s := &session{id: id, outbox: make(chan mcp.Response, 16)}
	if u, err := auth.User(ctx); err == nil {
		s.tenantID, s.userID = u.TenantID, u.ID
	}
	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()
	return s
}

func (r *sessionRegistry) get(id string) (*session, bool) {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	return s, ok
}

// close removes the session and shuts its outbox so the SSE pusher
// loop exits. Called when the GET connection's ctx is Done.
func (r *sessionRegistry) close(id string) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if ok {
		s.shut()
	}
}
