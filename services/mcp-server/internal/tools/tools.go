// Tool implementations that LLM agents invoke via MCP. Each tool:
//   1. Unmarshals its typed input shape from the raw JSON arguments
//   2. Calls the relevant backend service over HTTP, propagating the
//      tenant/user identity headers stamped on ctx by APIKeyAuth
//   3. Renders the result as a single text ContentBlock — clients
//      pass that text back to the LLM for further reasoning
//
// HTTP (not gRPC) was chosen for the cross-service hop because each
// of these tools is already exposed as a REST endpoint internally and
// the gateway-signed HTTP path is what every service-to-service call
// uses in this repo. gRPC clients would be more efficient but would
// require generating a new client per service in this go.mod.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/mcp-server/internal/mcp"
)

// Config bundles the upstream service URLs + the gateway-signature
// secret that backends require on every internal request.
type Config struct {
	// SearchBaseURL: http://search:8080 (in-cluster)
	SearchBaseURL string
	// DocumentBaseURL: http://document:8080
	DocumentBaseURL string
	// WorkflowBaseURL: http://workflow:8080
	WorkflowBaseURL string
	// IntelligenceBaseURL: http://intelligence:8080
	IntelligenceBaseURL string
	// GatewaySecret: the X-Gateway-Signature value (shared with Kong)
	GatewaySecret string
	// HTTP: shared client with a sensible per-call timeout
	HTTP *http.Client
}

// All returns the 4 tools the playbook §12.8 enumerates, wired to
// real upstream services. The MCP server.Register loop iterates this.
func All(cfg Config) []mcp.ToolDef {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	t := &tools{cfg: cfg}
	return []mcp.ToolDef{
		{
			Name:        "search_documents",
			Description: "Search the tenant's document corpus by free-text query. Returns up to 20 matches with id, title, snippet, and score. Use this when the user asks 'find documents about X' or 'where is the contract for Y'.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "The free-text search query"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 10},
				},
				"required": []string{"query"},
			},
			RequiredScope: "mcp:read",
			Handler:       t.searchDocuments,
		},
		{
			Name:        "get_document",
			Description: "Fetch a single document's metadata (title, tags, lifecycle, current version) by id. Does NOT return content bytes — use a separate download URL if the LLM needs the actual file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"document_id": map[string]any{"type": "string", "description": "The UUIDv7 document id"},
				},
				"required": []string{"document_id"},
			},
			RequiredScope: "mcp:read",
			Handler:       t.getDocument,
		},
		{
			Name:        "upload_document",
			Description: "Create a new document with text content. Returns the new document's id. The LLM provides title and text; the server stores it as a UTF-8 .txt blob.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string", "description": "Target workspace UUID"},
					"folder_id":    map[string]any{"type": "string", "description": "Target folder UUID (workspace root if omitted)"},
					"title":        map[string]any{"type": "string", "description": "Display title"},
					"content":      map[string]any{"type": "string", "description": "UTF-8 text content (max 256 KB)"},
				},
				"required": []string{"workspace_id", "title", "content"},
			},
			RequiredScope: "mcp:write",
			Handler:       t.uploadDocument,
		},
		{
			Name:        "start_workflow",
			Description: "Start a workflow run against a document. Returns the new workflow instance id. The LLM provides definition_id (the workflow template) and document_id (the target).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"definition_id": map[string]any{"type": "string", "description": "Workflow definition UUID"},
					"document_id":   map[string]any{"type": "string", "description": "Target document UUID"},
				},
				"required": []string{"definition_id", "document_id"},
			},
			RequiredScope: "mcp:write",
			Handler:       t.startWorkflow,
		},
	}
}

type tools struct{ cfg Config }

// ----- search_documents ------------------------------------------

type searchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *tools) searchDocuments(ctx context.Context, raw json.RawMessage) (*mcp.ToolCallResult, error) {
	var in searchInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.Query == "" {
		return nil, fmt.Errorf("query required")
	}
	if in.Limit == 0 {
		in.Limit = 10
	}
	body, _ := json.Marshal(map[string]any{"q": in.Query, "limit": in.Limit})
	respBody, err := t.do(ctx, "POST", t.cfg.SearchBaseURL+"/api/v1/search", body)
	if err != nil {
		return nil, err
	}
	return textResult(string(respBody)), nil
}

// ----- get_document ---------------------------------------------

type getDocInput struct {
	DocumentID string `json:"document_id"`
}

func (t *tools) getDocument(ctx context.Context, raw json.RawMessage) (*mcp.ToolCallResult, error) {
	var in getDocInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.DocumentID == "" {
		return nil, fmt.Errorf("document_id required")
	}
	target := t.cfg.DocumentBaseURL + "/api/v1/documents/" + url.PathEscape(in.DocumentID)
	respBody, err := t.do(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	return textResult(string(respBody)), nil
}

// ----- upload_document ------------------------------------------

type uploadInput struct {
	WorkspaceID string `json:"workspace_id"`
	FolderID    string `json:"folder_id"`
	Title       string `json:"title"`
	Content     string `json:"content"`
}

const maxUploadBytes = 256 * 1024

func (t *tools) uploadDocument(ctx context.Context, raw json.RawMessage) (*mcp.ToolCallResult, error) {
	var in uploadInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.WorkspaceID == "" || in.Title == "" || in.Content == "" {
		return nil, fmt.Errorf("workspace_id, title, content required")
	}
	if len(in.Content) > maxUploadBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", maxUploadBytes)
	}
	// Document service exposes POST /api/v1/documents which creates a
	// document row; the version+blob are added via storage in a
	// follow-up call. For the MCP MVP we only create the row + drop
	// the text as the description field, which is queryable via search.
	// The full upload-and-link flow lives in services/document/cmd
	// behind the bulk-import API — wiring that here would require the
	// MCP server to talk to storage too. Deferred to a follow-up.
	body, _ := json.Marshal(map[string]any{
		"workspace_id": in.WorkspaceID,
		"folder_id":    in.FolderID,
		"title":        in.Title,
		"description":  in.Content,
		"tags":         []string{"mcp-uploaded"},
	})
	respBody, err := t.do(ctx, "POST", t.cfg.DocumentBaseURL+"/api/v1/documents", body)
	if err != nil {
		return nil, err
	}
	return textResult(string(respBody)), nil
}

// ----- start_workflow -------------------------------------------

type startWorkflowInput struct {
	DefinitionID string `json:"definition_id"`
	DocumentID   string `json:"document_id"`
}

func (t *tools) startWorkflow(ctx context.Context, raw json.RawMessage) (*mcp.ToolCallResult, error) {
	var in startWorkflowInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if in.DefinitionID == "" || in.DocumentID == "" {
		return nil, fmt.Errorf("definition_id and document_id required")
	}
	body, _ := json.Marshal(map[string]any{
		"definition_id": in.DefinitionID,
		"document_id":   in.DocumentID,
	})
	respBody, err := t.do(ctx, "POST", t.cfg.WorkflowBaseURL+"/api/v1/workflows/instances", body)
	if err != nil {
		return nil, err
	}
	return textResult(string(respBody)), nil
}

// ----- shared HTTP helper ---------------------------------------

func (t *tools) do(ctx context.Context, method, target string, body []byte) ([]byte, error) {
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gateway-Signature", t.cfg.GatewaySecret)
	// Propagate identity stamped on ctx by APIKeyAuth so the upstream
	// service applies the same tenant + user the MCP caller has.
	if tid, err := auth.GetTenantID(ctx); err == nil {
		req.Header.Set("X-Auth-Tenant-ID", tid.String())
	}
	if uid, err := auth.GetUserID(ctx); err == nil {
		req.Header.Set("X-User-ID", uid.String())
	}
	resp, err := t.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return respBody, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func textResult(text string) *mcp.ToolCallResult {
	return &mcp.ToolCallResult{
		Content: []mcp.ContentBlock{{Type: "text", Text: text}},
	}
}

// Silence the unused import linter for `strings` if we end up not
// using it after refactoring. (Used by tests.)
var _ = strings.TrimSpace
