// ADR 0075 — bulk import + export client. Talks to the document
// service's NDJSON facade (POST /api/v1/admin/bulk/import,
// GET /api/v1/admin/bulk/export). Bypasses the axios api client
// because both endpoints stream NDJSON and we want to consume the
// response line-by-line without buffering it all in memory.

export type BulkResource = 'workspace' | 'folder' | 'document' | 'user' | 'group'

export interface BulkItemResult {
  external_id?: string
  success: boolean
  internal_id?: string
  error?: string
  skipped?: boolean
}

export interface BulkImportSummary {
  totalLines: number
  successCount: number
  failureCount: number
  results: BulkItemResult[]
  // Free-form errors that aren't tied to a single line — JSON parse
  // errors, truncated batches, server panic responses.
  globalErrors: string[]
}

// streamImport posts NDJSON to the import facade and yields each
// per-item Result as it arrives. Returns a final summary so the UI
// can render counts when the stream closes.
export async function streamImport(
  body: string,
  onResult?: (r: BulkItemResult) => void,
  signal?: AbortSignal,
): Promise<BulkImportSummary> {
  const res = await fetch('/api/v1/admin/bulk/import', {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/x-ndjson' },
    body,
    signal,
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    throw new Error(`bulk import ${res.status}: ${text || res.statusText}`)
  }
  const summary: BulkImportSummary = {
    totalLines: 0, successCount: 0, failureCount: 0, results: [], globalErrors: [],
  }
  await consumeNDJSON(res.body, (obj) => {
    if (typeof obj === 'object' && obj !== null && 'error' in (obj as Record<string, unknown>) && !('success' in (obj as Record<string, unknown>))) {
      summary.globalErrors.push(String((obj as { error?: unknown }).error))
      return
    }
    const r = obj as BulkItemResult
    summary.totalLines++
    if (r.success) summary.successCount++
    else summary.failureCount++
    summary.results.push(r)
    onResult?.(r)
  })
  return summary
}

export interface BulkExportFilter {
  resource: BulkResource
  workspaceId?: string
  from?: string  // RFC3339
  to?: string    // RFC3339
}

// streamExport opens the export endpoint and yields one BulkItem
// per line. Returns the full set of items so callers can hand the
// list to file-saver / clipboard / next pipeline stage.
export async function streamExport(
  filter: BulkExportFilter,
  onItem?: (item: unknown) => void,
  signal?: AbortSignal,
): Promise<unknown[]> {
  const params = new URLSearchParams()
  params.set('resource', filter.resource)
  if (filter.workspaceId) params.set('workspace_id', filter.workspaceId)
  if (filter.from) params.set('from', filter.from)
  if (filter.to) params.set('to', filter.to)
  const res = await fetch(`/api/v1/admin/bulk/export?${params.toString()}`, {
    method: 'GET',
    credentials: 'include',
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    throw new Error(`bulk export ${res.status}: ${text || res.statusText}`)
  }
  const out: unknown[] = []
  await consumeNDJSON(res.body, (obj) => {
    out.push(obj)
    onItem?.(obj)
  }, signal)
  return out
}

// consumeNDJSON reads a ReadableStream of UTF-8 bytes line-by-line
// and invokes the callback for every JSON-decoded object. Tolerates
// CRLF line endings and gracefully ignores empty lines.
async function consumeNDJSON(
  body: ReadableStream<Uint8Array>,
  onObj: (obj: unknown) => void,
  signal?: AbortSignal,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder('utf-8')
  let buf = ''
  try {
    while (true) {
      if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
      const { value, done } = await reader.read()
      if (done) break
      buf += decoder.decode(value, { stream: true })
      let idx = buf.indexOf('\n')
      while (idx >= 0) {
        const line = buf.slice(0, idx).trim()
        buf = buf.slice(idx + 1)
        if (line.length > 0) {
          try {
            onObj(JSON.parse(line))
          } catch {
            onObj({ error: `malformed json line: ${line.slice(0, 160)}` })
          }
        }
        idx = buf.indexOf('\n')
      }
    }
    const tail = buf.trim()
    if (tail.length > 0) {
      try { onObj(JSON.parse(tail)) }
      catch { onObj({ error: `malformed trailing json: ${tail.slice(0, 160)}` }) }
    }
  } finally {
    reader.releaseLock()
  }
}
