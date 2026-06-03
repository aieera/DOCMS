# ADR 0091 — MCP server for LLM agents (§12.8)

**Status:** Accepted. New service `services/mcp-server` shipped.

**Date:** 2026-05-18

## Context

Blueprint §12.8 calls for a Model Context Protocol server that exposes
SeDoc as a tool surface for LLM agents (Claude Desktop, Cursor,
GitHub Copilot, future internal agents). The playbook listed:

- `services/mcp-server` (new top-level service)
- Tools: `search_documents`, `get_document`, `upload_document`, `start_workflow`
- Auth: API key with explicit scopes
- Audit: every call as `mcp.tool.<name>.invoked`
- Frontend `/admin/integrations/mcp` with install snippets

The original scaffolding lived inside `services/connector/internal/mcp/`
and was a hand-rolled JSON-RPC-over-SSE server with stub tool handlers
returning `{"status":"ok"}`. It did NOT implement the official MCP
handshake (`initialize`, `tools/list`, `tools/call`), so real LLM
clients refused to recognise it.

The playbook's "ADR 0082" reference is stale — that number is taken by
faceted-search. We use **ADR 0091**.

## Decision

### Extract to a dedicated service

`services/mcp-server/` is a new top-level Go service with its own
Dockerfile, go.mod, and compose entry. Reason: MCP traffic is a
distinct hot path from connector concerns (webhooks, email intake,
native iPaaS connectors), and the security surface — Bearer API key
for external LLM clients — is different from the connector service's
session-cookie + gateway-sig pattern.

The old `services/connector/internal/mcp/` package and its route
mount in connector's handler are removed. Connector's `handler.New`
signature dropped the `mcpSrv` parameter; documented inline.

### Spec compliance

The new server implements MCP protocol version `2024-11-05`:

- `initialize` — handshake with protocol version + capabilities
- `tools/list` — enumerate tools with JSON Schema for arguments
- `tools/call` — dispatch by name with typed argument unmarshal
- `ping`, `notifications/initialized` — heartbeat + lifecycle

Both HTTP variants of the official remote transport are exposed:

- `POST /api/v1/mcp` — single JSON-RPC frame, JSON response. Used by
  curl / the admin "Test" button.
- `GET /api/v1/mcp/sse` — long-poll SSE stream for server→client push.
- `POST /api/v1/mcp/sse` — POST companion for SSE clients (Cursor,
  Claude Desktop's HTTP/SSE transport).

### Tools

Four tools — playbook spec exactly:

| Tool | Required scope | Backend service |
|---|---|---|
| `search_documents` | `mcp:read` | search service `POST /api/v1/search` |
| `get_document` | `mcp:read` | document service `GET /api/v1/documents/{id}` |
| `upload_document` | `mcp:write` | document service `POST /api/v1/documents` (text content as description; full upload+version deferred) |
| `start_workflow` | `mcp:write` | workflow service `POST /api/v1/workflows/instances` |

Each tool unmarshals into a typed input struct, calls the upstream
service over HTTP with `X-Gateway-Signature` + propagated `X-Auth-Tenant-ID`
and `X-User-ID` headers (stamped on ctx by `APIKeyAuth`), and renders
the result as a single text `ContentBlock`.

### Authentication

`pkg/middleware/apikey.go` (built for ADR 0090 iPaaS) is reused. Route-
level scope is `mcp:read`; per-tool scopes are checked inside the
dispatcher via a new `auth.WithScopes` / `auth.GetScopes` context pair
added to `pkg/auth/context.go`. `mcp:*` grants both.

API key issuance reuses the existing `/auth/api-keys` endpoints — the
admin UI just lists keys whose scopes start with `mcp:`.

### Audit

Every tool call emits `dms.audit.mcp_tool_invoked.v1` to NATS with
payload `{tenant_id, user_id, tool, ok, error, source, emitted_at}`.
Fire-and-forget; failures log but never block the response. The audit
service's existing consumer indexes this into the audit ledger.

### Frontend

`/admin/integrations/mcp` page:

- Server-health probe via `tools/list` JSON-RPC call
- API key list filtered to keys with `mcp:*` scope
- Create-key dialog with scope multi-select (read / write / both)
- Copy-once plaintext reveal
- Install-snippet cards for Claude Desktop / Cursor / GitHub Copilot,
  each with the right config file path + JSON template (replace
  `{{API_KEY}}`)

Tile on `/admin` and chip on `/admin/integrations` index page.

## Out of scope

- **Streaming tool responses.** Tool handlers return one shot; streaming
  multi-step output (e.g. partial search results) would need a different
  transport contract.
- **Server-initiated notifications.** `GET /sse` holds the stream open
  but we never push — no `notifications/resources/updated` etc. emitted.
- **Resource + Prompt MCP primitives.** Spec defines `resources/*` and
  `prompts/*` alongside `tools/*`. We implement tools only.
- **`upload_document` with binary content.** Today it stores text content
  in the description field. Full multipart upload+version through
  storage is a follow-up.
- **Playwright spec.** Deferred — testing MCP via Playwright requires
  scaffolding a fake LLM client; current smoke coverage is JSON-RPC
  curl against the server.
- **Rate limiting** per tool/per key. Same workstream as the rest of
  the rate-limit story.

## File map

| Path | Purpose |
|---|---|
| `services/mcp-server/go.mod`, `cmd/server/main.go` | Service entrypoint, NATS+pool wiring |
| `services/mcp-server/internal/mcp/protocol.go` | JSON-RPC + MCP type definitions |
| `services/mcp-server/internal/mcp/server.go` | Dispatcher, registry, scope check |
| `services/mcp-server/internal/handler/handler.go` | HTTP + SSE transport |
| `services/mcp-server/internal/tools/tools.go` | 4 tool implementations |
| `pkg/auth/context.go` | `WithScopes` + `GetScopes` helpers |
| `pkg/middleware/apikey.go` | Stashes scopes on ctx after key lookup |
| `docker-compose.yml` | New `mcp-server` container (port 8192) |
| `web/vite.config.ts` | `/api/v1/mcp` → `localhost:8192` proxy |
| `web/src/api/mcp.ts` | Tools-list probe client |
| `web/src/routes/_authenticated/admin/integrations/mcp.tsx` | Admin page |
| `go.work` | Added `./services/mcp-server` |

## Acceptance signal

```bash
# 1. Create an MCP key via the admin UI with scope mcp:*

# 2. tools/list returns 4 tools
curl -X POST http://localhost:3000/api/v1/mcp \
  -H "Authorization: Bearer vdms_..." \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

# 3. tools/call dispatches to upstream
curl -X POST http://localhost:3000/api/v1/mcp \
  -H "Authorization: Bearer vdms_..." \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call",
       "params":{"name":"search_documents","arguments":{"query":"contract"}}}'

# 4. dms.audit.mcp_tool_invoked.v1 emitted (verify with `nats sub`)
```
