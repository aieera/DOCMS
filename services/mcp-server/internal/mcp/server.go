// MCP JSON-RPC dispatcher. Handles initialize / tools/list / tools/call
// against the tool registry. Transport-agnostic — the HTTP handler in
// services/mcp-server/internal/handler wraps this with an SSE layer.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
)

// ToolFunc executes a tool. ctx carries tenant + user identity stamped
// by APIKeyAuth; arguments is the raw `arguments` field from the
// tools/call params (each tool unmarshals into its own typed struct).
type ToolFunc func(ctx context.Context, arguments json.RawMessage) (*ToolCallResult, error)

// ToolDef bundles metadata + handler. RequiredScope gates per-tool
// access — e.g. upload_document needs mcp:write, search needs mcp:read.
type ToolDef struct {
	Name          string
	Description   string
	InputSchema   map[string]any
	RequiredScope string
	Handler       ToolFunc
}

// Server holds the tool registry. Construction injects audit emit so
// every tool call lands in dms.audit.mcp_tool_invoked.v1 regardless of
// success/failure.
type Server struct {
	tools     map[string]ToolDef
	mu        sync.RWMutex
	log       zerolog.Logger
	auditEmit func(ctx context.Context, toolName string, ok bool, errMsg string)
	// scopeOf reads the caller's scopes from ctx (stamped by APIKeyAuth).
	scopeOf func(ctx context.Context) []string
}

// Config bundles deps.
type Config struct {
	Logger    zerolog.Logger
	AuditEmit func(ctx context.Context, toolName string, ok bool, errMsg string)
	ScopeOf   func(ctx context.Context) []string
}

// New constructs an empty server. Caller registers tools via Register.
func New(cfg Config) *Server {
	if cfg.AuditEmit == nil {
		cfg.AuditEmit = func(context.Context, string, bool, string) {}
	}
	if cfg.ScopeOf == nil {
		cfg.ScopeOf = func(context.Context) []string { return nil }
	}
	return &Server{
		tools:     make(map[string]ToolDef),
		log:       cfg.Logger,
		auditEmit: cfg.AuditEmit,
		scopeOf:   cfg.ScopeOf,
	}
}

// Register adds a tool. Panics on name collision so a double-wire bug
// trips at boot rather than producing silently-shadowed handlers.
func (s *Server) Register(t ToolDef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tools[t.Name]; exists {
		panic("mcp: tool already registered: " + t.Name)
	}
	s.tools[t.Name] = t
}

// Dispatch routes a single JSON-RPC request. Returns the response
// (which the SSE transport emits as an `event: message` frame).
func (s *Server) Dispatch(ctx context.Context, req Request) Response {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "initialized", "notifications/initialized":
		// MCP "initialized" notification has no id and expects no response.
		// We acknowledge with an empty result so SSE clients see something.
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "ping":
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	}
	return Response{
		JSONRPC: "2.0", ID: req.ID,
		Error: &Error{Code: -32601, Message: "method not found: " + req.Method},
	}
}

func (s *Server) handleInitialize(req Request) Response {
	return Response{
		JSONRPC: "2.0", ID: req.ID,
		Result: InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities: map[string]any{
				// listChanged: false — we don't push tool-list deltas mid-session.
				"tools": map[string]any{"listChanged": false},
			},
			ServerInfo: map[string]any{
				"name":    "vaultdms-mcp",
				"version": "0.1.0",
			},
		},
	}
}

func (s *Server) handleToolsList(req Request) Response {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Tool, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return Response{JSONRPC: "2.0", ID: req.ID, Result: ToolsListResult{Tools: out}}
}

func (s *Server) handleToolsCall(ctx context.Context, req Request) Response {
	var p ToolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, ErrCodeInvalidArgument, "invalid params: "+err.Error())
	}
	s.mu.RLock()
	t, ok := s.tools[p.Name]
	s.mu.RUnlock()
	if !ok {
		s.auditEmit(ctx, p.Name, false, "tool not found")
		return errResp(req.ID, ErrCodeToolNotFound, "tool not found: "+p.Name)
	}
	// Per-tool scope check on top of the API-key-wide gate that the
	// middleware already enforced (mcp:* covers everything; otherwise
	// each tool requires a specific scope).
	if t.RequiredScope != "" && !hasScope(s.scopeOf(ctx), t.RequiredScope) {
		s.auditEmit(ctx, p.Name, false, "missing scope")
		return errResp(req.ID, ErrCodeMissingScope, "missing scope: "+t.RequiredScope)
	}
	result, err := t.Handler(ctx, p.Arguments)
	if err != nil {
		s.auditEmit(ctx, p.Name, false, err.Error())
		return errResp(req.ID, ErrCodeToolExecFailed, fmt.Sprintf("%s failed: %v", p.Name, err))
	}
	s.auditEmit(ctx, p.Name, true, "")
	return Response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func errResp(id json.RawMessage, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &Error{Code: code, Message: msg}}
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want || s == "mcp:*" {
			return true
		}
	}
	return false
}

// ErrUnknownTool is returned by callers that try to inspect a tool by
// name when it isn't registered. Useful for tests + admin endpoints.
var ErrUnknownTool = errors.New("mcp: unknown tool")
