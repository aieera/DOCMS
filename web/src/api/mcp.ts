// MCP server health + tool-list probe (ADR 0091). API key admin reuses
// the existing /auth/api-keys endpoints; this client is only for the
// page's diagnostics ("server reachable? what tools does it expose?").
//
// IMPORTANT: we deliberately use raw `fetch` here instead of the shared
// `api` axios instance. The MCP endpoint requires a Bearer API key —
// session cookies alone return 401. The shared axios response
// interceptor logs the user out on ANY 401, which would kick them off
// the admin page every time they visit before setting up a key. The
// probe being a soft check ("server reachable?") means failure is
// expected and acceptable; rendering an "Unreachable" state is the
// correct UX, not a logout.
export interface MCPTool {
  name: string
  description: string
  inputSchema: Record<string, unknown>
}

interface JSONRPCResponse<T> {
  jsonrpc: '2.0'
  id: number
  result?: T
  error?: { code: number; message: string }
}

export async function listMCPTools(): Promise<MCPTool[]> {
  try {
    const resp = await fetch('/api/v1/mcp', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/list' }),
      credentials: 'include',
    })
    if (!resp.ok) return [] // 401, 503, etc. — treat as "unreachable"
    const data: JSONRPCResponse<{ tools: MCPTool[] }> = await resp.json()
    if (data.error) return []
    return data.result?.tools ?? []
  } catch {
    return []
  }
}
