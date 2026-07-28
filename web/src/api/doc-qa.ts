import { api } from './client'
import { useAuthStore } from '@/store/authStore'

export interface Citation {
  chunk_index: number
  text: string
  page?: number
  start_char?: number
  end_char?: number
  similarity_score: number
  document_id: string
  version_id: string
}

export interface QAMessage {
  id: string
  role: 'user' | 'assistant'
  content: string
  citations: Citation[]
  model_used: string
  tokens_used: number
  created_at: string
}

export interface QAConversation {
  id: string
  title: string
  created_at: string
  updated_at: string
}

export type QAStreamEvent =
  | { type: 'conversation'; conversation_id: string }
  | { type: 'citations'; citations: Citation[] }
  | { type: 'chunk'; text: string }
  | { type: 'done'; full_text: string; citations: Citation[]; model: string; input_tokens: number; output_tokens: number; cost_usd: number; elapsed_ms: number }
  | { type: 'error'; message: string }

interface AskParams {
  documentId: string
  question: string
  conversationId?: string
  model?: string
  signal?: AbortSignal
  onEvent: (event: QAStreamEvent) => void
}

/**
 * Open the SSE Q&A stream. Returns once the stream completes (or aborts).
 * Caller drives state via onEvent. Errors during the request itself are
 * thrown; per-event errors arrive as type='error' through onEvent.
 */
export async function streamQA(params: AskParams): Promise<void> {
  const { documentId, question, conversationId, model, signal, onEvent } = params
  const baseURL = api.defaults.baseURL ?? '/api/v1'
  const { tenantId, user } = useAuthStore.getState()
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    Accept: 'text/event-stream',
  }
  // The axios interceptor injects identity headers on every request,
  // but fetch() bypasses interceptors — so we mirror the same shape
  // here. The intelligence service reads X-Tenant-ID + X-User-ID
  // directly (no session-cookie path), and 400s without them.
  if (tenantId) {
    headers['X-Auth-Tenant-ID'] = tenantId
    headers['X-Tenant-ID'] = tenantId
  }
  if (user?.id)   headers['X-User-ID']   = user.id
  if (user?.role) headers['X-User-Role'] = user.role
  const resp = await fetch(`${baseURL}/intelligence/qa`, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({
      document_id: documentId,
      question,
      conversation_id: conversationId,
      model,
    }),
    signal,
  })
  if (!resp.ok || !resp.body) {
    throw new Error(`qa stream failed: ${resp.status}`)
  }
  const reader = resp.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  // Idle watchdog: if the socket dies without an EOF (proxy drop,
  // backend restart mid-stream), reader.read() waits forever, the
  // caller's `streaming` flag never clears, and the chat silently
  // swallows every later question until a page reload. The server
  // heartbeats via chunks while generating, so a long silent gap
  // means the stream is dead — abort it so the caller's error path
  // runs and the UI recovers.
  const IDLE_TIMEOUT_MS = 90_000
  const readWithIdleTimeout = async () => {
    let timer: ReturnType<typeof setTimeout> | undefined
    try {
      return await Promise.race([
        reader.read(),
        new Promise<never>((_, reject) => {
          timer = setTimeout(() => {
            reader.cancel().catch(() => {})
            reject(new Error('The answer stream went quiet for 90s and was closed. Try asking again.'))
          }, IDLE_TIMEOUT_MS)
        }),
      ])
    } finally {
      clearTimeout(timer)
    }
  }
  while (true) {
    const { value, done } = await readWithIdleTimeout()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    // Parse SSE events terminated by `\n\n`.
    let idx
    while ((idx = buf.indexOf('\n\n')) !== -1) {
      const block = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      const dataLine = block.split('\n').find((l) => l.startsWith('data: '))
      if (!dataLine) continue
      try {
        const evt = JSON.parse(dataLine.slice(6)) as QAStreamEvent
        onEvent(evt)
      } catch {
        // ignore malformed event line
      }
    }
  }
}

export async function askQASync(params: {
  documentId: string
  question: string
  conversationId?: string
  model?: string
}) {
  const { data } = await api.post<{
    conversation_id: string
    answer: string
    citations: Citation[]
    model: string
    output_tokens: number
  }>('/intelligence/qa/sync', {
    document_id: params.documentId,
    question: params.question,
    conversation_id: params.conversationId,
    model: params.model,
  })
  return data
}

export async function getQAHistory(documentId: string, conversationId?: string) {
  const { data } = await api.get<{
    conversations: QAConversation[]
    messages?: QAMessage[]
  }>(`/intelligence/qa/history/${documentId}`, {
    params: conversationId ? { conversation_id: conversationId } : undefined,
  })
  return data
}
