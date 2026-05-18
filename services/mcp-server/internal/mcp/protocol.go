// Package mcp implements the Model Context Protocol server per the
// official spec (https://modelcontextprotocol.io/). Wire format:
// JSON-RPC 2.0 framed over an HTTP+SSE channel. The three core
// methods this server implements:
//
//   initialize → handshake; client posts its supported protocol
//                version + capabilities, server responds with its
//                own. Both sides MUST complete this before any
//                tools/call can succeed.
//
//   tools/list → enumerate available tools with names + descriptions
//                + input JSON Schemas. Clients use this to populate
//                their UI.
//
//   tools/call → invoke a tool by name with arguments. Server runs
//                the tool, returns either content blocks or an error.
//
// The earlier hand-rolled scaffold in services/connector/internal/mcp/
// did not implement initialize / tools/list — meaning Claude Desktop,
// Cursor, and any other compliant client refused to recognise it as
// an MCP server. This package fixes that.
package mcp

import "encoding/json"

// ProtocolVersion is the MCP protocol revision this server supports.
// Anthropic publishes a date-stamped version per release; we match
// the one Claude Desktop ships at the time of writing.
const ProtocolVersion = "2024-11-05"

// Request is a JSON-RPC 2.0 request frame.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response frame.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error follows JSON-RPC 2.0 error semantics. MCP-specific codes
// live in the -32000 to -32099 range (server-defined errors).
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Server-defined error codes used by this implementation.
const (
	ErrCodeToolNotFound    = -32001
	ErrCodeToolExecFailed  = -32002
	ErrCodeMissingScope    = -32003
	ErrCodeInvalidArgument = -32004
)

// InitializeParams is what the client sends on the first call.
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]any         `json:"capabilities"`
	ClientInfo      map[string]any         `json:"clientInfo,omitempty"`
}

// InitializeResult is what we send back. capabilities.tools must be
// present for clients to enable the tools panel.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ServerInfo      map[string]any `json:"serverInfo"`
}

// Tool describes one callable tool. InputSchema is a JSON Schema
// document for the tool's arguments. The official MCP clients use
// this to validate calls before dispatching.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolsListResult is the response to tools/list.
type ToolsListResult struct {
	Tools []Tool `json:"tools"`
}

// ToolCallParams is the inbound shape for tools/call.
type ToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolCallResult — content blocks per the MCP spec. Text blocks are
// what every client renders today; image/resource blocks land later.
type ToolCallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// ContentBlock is one block of tool output. type=="text" carries
// human-readable string output; clients display this to the LLM.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
