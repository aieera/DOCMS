// Package mcp implements an MCP (Model Context Protocol) server that exposes
// VaultDMS as a set of tools over JSON-RPC via SSE. Authenticated with API keys.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/connector/internal/model"
)

// ToolHandler is a function that executes an MCP tool.
type ToolHandler func(tenantID string, params map[string]any) (any, error)

// Server handles MCP JSON-RPC over SSE.
type Server struct {
	tools map[string]ToolHandler
	log   zerolog.Logger
}

// NewServer creates an MCP server with the default tool set.
func NewServer(log zerolog.Logger) *Server {
	s := &Server{tools: make(map[string]ToolHandler), log: log}
	s.tools["search_documents"] = s.searchDocuments
	s.tools["get_document"] = s.getDocument
	s.tools["upload_document"] = s.uploadDocument
	s.tools["start_workflow"] = s.startWorkflow
	s.tools["ask_question"] = s.askQuestion
	return s
}

// HandleSSE is the HTTP handler for the MCP SSE endpoint.
func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	apiKey := r.Header.Get("X-API-Key")
	if tenantID == "" || apiKey == "" {
		http.Error(w, `{"error":"X-Tenant-ID and X-API-Key required"}`, http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send capabilities.
	toolList := make([]map[string]string, 0, len(s.tools))
	for name := range s.tools {
		toolList = append(toolList, map[string]string{"name": name})
	}
	s.sendSSE(w, flusher, "capabilities", map[string]any{
		"tools":          toolList,
		"protocol":       "mcp",
		"server_version": "1.0.0",
	})

	// Read JSON-RPC requests from query or POST body.
	decoder := json.NewDecoder(r.Body)
	for {
		var call model.MCPToolCall
		if err := decoder.Decode(&call); err != nil {
			return // client disconnected
		}
		result := s.handleCall(tenantID, call)
		s.sendSSE(w, flusher, "result", result)
	}
}

func (s *Server) handleCall(tenantID string, call model.MCPToolCall) model.MCPToolResult {
	handler, ok := s.tools[call.Method]
	if !ok {
		return model.MCPToolResult{
			JSONRPC: "2.0", ID: call.ID,
			Error: &model.MCPError{Code: -32601, Message: "tool not found: " + call.Method},
		}
	}
	result, err := handler(tenantID, call.Params)
	if err != nil {
		return model.MCPToolResult{
			JSONRPC: "2.0", ID: call.ID,
			Error: &model.MCPError{Code: -32000, Message: err.Error()},
		}
	}
	return model.MCPToolResult{JSONRPC: "2.0", ID: call.ID, Result: result}
}

func (s *Server) sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	raw, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
	flusher.Flush()
}

// ---- Tool implementations (stubs that call internal services) -------------

func (s *Server) searchDocuments(_ string, params map[string]any) (any, error) {
	query, _ := params["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("query parameter required")
	}
	return map[string]any{"status": "ok", "query": query, "results": []any{}}, nil
}

func (s *Server) getDocument(_ string, params map[string]any) (any, error) {
	id, _ := params["document_id"].(string)
	if id == "" {
		return nil, fmt.Errorf("document_id required")
	}
	return map[string]any{"status": "ok", "document_id": id}, nil
}

func (s *Server) uploadDocument(_ string, params map[string]any) (any, error) {
	filename, _ := params["filename"].(string)
	if filename == "" {
		return nil, fmt.Errorf("filename required")
	}
	return map[string]any{"status": "ok", "filename": filename}, nil
}

func (s *Server) startWorkflow(_ string, params map[string]any) (any, error) {
	defID, _ := params["definition_id"].(string)
	docID, _ := params["document_id"].(string)
	if defID == "" || docID == "" {
		return nil, fmt.Errorf("definition_id and document_id required")
	}
	return map[string]any{"status": "ok", "definition_id": defID, "document_id": docID}, nil
}

func (s *Server) askQuestion(_ string, params map[string]any) (any, error) {
	question, _ := params["question"].(string)
	if question == "" {
		return nil, fmt.Errorf("question required")
	}
	return map[string]any{"status": "ok", "question": question, "answer": "stub"}, nil
}

// silence unused
var _ = strings.TrimSpace
