import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import { Sparkles, Send, Plus, Globe, Building2, User as UserIcon } from 'lucide-react'

import { getWorkspaces } from '@/api/workspaces'
import {
  streamQA, getAskHistory,
  type Citation, type QAConversation, type QAScope,
} from '@/api/doc-qa'
import { PageHeader } from '@/components/shared/PageHeader'
import { AnswerMarkdown } from '@/components/ai/AnswerMarkdown'
import { Button } from '@/components/ui/shadcn/button'
import { Textarea } from '@/components/ui/shadcn/textarea'
import {
  Select as SelectRoot, SelectTrigger, SelectValue, SelectContent, SelectItem,
} from '@/components/ui/shadcn/select'
import { Spinner } from '@/components/ui/Spinner'
import { formatRelativeTime } from '@/lib/formatters'
import { cn } from '@/lib/cn'

const ALL_WORKSPACES = '__all__'
// Mirrors the backend cap in services/intelligence/app/api/routes.py.
const MAX_QUESTION_LEN = 4000

const EXAMPLE_QUESTIONS = [
  'What documents are available?',
  'Summarize the key terms of the latest contract.',
  'What are the main obligations in the partner program?',
  'Which documents mention renewal or termination?',
]

interface UIMessage {
  id: string
  role: 'user' | 'assistant'
  content: string
  citations?: Citation[]
  pending?: boolean
}

function AskPage() {
  const qc = useQueryClient()
  const [input, setInput] = useState('')
  const [workspaceId, setWorkspaceId] = useState<string>(ALL_WORKSPACES)
  const [activeConv, setActiveConv] = useState<QAConversation | null>(null)
  const [messages, setMessages] = useState<UIMessage[]>([])
  const [streaming, setStreaming] = useState(false)
  const [loadingConv, setLoadingConv] = useState(false)
  const idc = useRef(0)
  const nextId = () => `m${++idc.current}`
  const threadRef = useRef<HTMLDivElement>(null)

  const workspaces = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })
  const history = useQuery({ queryKey: ['ask-history'], queryFn: () => getAskHistory() })

  // Keep the thread pinned to the latest turn as it streams.
  useEffect(() => {
    const el = threadRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages])

  const wsName = (id: string | null | undefined) =>
    workspaces.data?.find((w) => w.id === id)?.name ?? 'workspace'

  const newChat = () => {
    setActiveConv(null)
    setMessages([])
    setInput('')
  }

  const openConv = async (conv: QAConversation) => {
    if (streaming) return
    setActiveConv(conv)
    setMessages([])
    setLoadingConv(true)
    try {
      const data = await getAskHistory({ conversationId: conv.id })
      setMessages(
        (data.messages ?? []).map((m) => ({
          id: m.id, role: m.role, content: m.content, citations: m.citations,
        })),
      )
    } catch {
      toast.error("Couldn't load that conversation")
    } finally {
      setLoadingConv(false)
    }
  }

  const send = async (override?: string) => {
    const q = (override ?? input).trim()
    if (!q || streaming) return
    setInput('')

    const userId = nextId()
    const aId = nextId()
    setMessages((m) => [
      ...m,
      { id: userId, role: 'user', content: q },
      { id: aId, role: 'assistant', content: '', pending: true },
    ])
    setStreaming(true)

    // A conversation's scope is fixed at creation; existing chats reuse their
    // stored scope, new chats take it from the composer's selector.
    const isGlobal = activeConv
      ? (activeConv.scope ?? 'global') === 'global'
      : workspaceId === ALL_WORKSPACES
    const scope: QAScope = isGlobal ? 'global' : 'workspace'
    const wsId = isGlobal ? undefined : activeConv?.workspace_id ?? workspaceId
    const wasNew = !activeConv
    let convId = activeConv?.id

    try {
      await streamQA({
        question: q,
        scope,
        workspaceId: wsId ?? undefined,
        conversationId: convId,
        onEvent: (evt) => {
          if (evt.type === 'conversation') {
            convId = evt.conversation_id
          } else if (evt.type === 'citations') {
            setMessages((m) => m.map((x) => (x.id === aId ? { ...x, citations: evt.citations } : x)))
          } else if (evt.type === 'chunk') {
            setMessages((m) => m.map((x) => (x.id === aId ? { ...x, content: x.content + evt.text } : x)))
          } else if (evt.type === 'done') {
            setMessages((m) => m.map((x) => (x.id === aId
              ? { ...x, content: evt.full_text || x.content, citations: evt.citations ?? x.citations, pending: false }
              : x)))
          } else if (evt.type === 'error') {
            setMessages((m) => m.map((x) => (x.id === aId ? { ...x, content: `⚠️ ${evt.message}`, pending: false } : x)))
            toast.error(evt.message)
          }
        },
      })
    } catch (e) {
      setMessages((m) => m.map((x) => (x.id === aId
        ? { ...x, content: x.content || '⚠️ The request failed. Try again.', pending: false }
        : x)))
      toast.error(e instanceof Error ? e.message : 'Ask failed')
    } finally {
      setStreaming(false)
      // Adopt the server conversation id for a new chat so follow-ups thread,
      // and refresh the sidebar so the new/updated conversation appears.
      if (wasNew && convId) {
        setActiveConv({
          id: convId, title: q.slice(0, 60), scope,
          workspace_id: wsId ?? null, created_at: '', updated_at: '',
        })
      }
      qc.invalidateQueries({ queryKey: ['ask-history'] })
    }
  }

  const conversations = history.data?.conversations ?? []
  const scopeLabel = activeConv
    ? activeConv.scope === 'workspace'
      ? wsName(activeConv.workspace_id)
      : 'All workspaces'
    : workspaceId === ALL_WORKSPACES
      ? 'All workspaces'
      : wsName(workspaceId)

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-4 lg:overflow-hidden">
      <PageHeader
        title="Ask"
        description="Chat with your documents — answers cite the source pages."
      />

      <div className="grid min-h-0 flex-1 gap-4 lg:grid-cols-[260px_minmax(0,1fr)] lg:overflow-hidden">
        {/* History sidebar */}
        <aside className="flex min-h-0 flex-col rounded-lg border border-border bg-card lg:overflow-hidden">
          <div className="border-b border-border p-2">
            <Button
              variant="outline"
              className="w-full justify-start"
              onClick={newChat}
              data-testid="ask-new-chat"
            >
              <Plus className="h-4 w-4" /> New chat
            </Button>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-2" data-testid="ask-history">
            {history.isLoading ? (
              <div className="flex justify-center p-4"><Spinner /></div>
            ) : conversations.length === 0 ? (
              <p className="px-2 py-6 text-center text-xs text-muted-foreground">
                No conversations yet.
              </p>
            ) : (
              <ul className="space-y-0.5">
                {conversations.map((c) => (
                  <li key={c.id}>
                    <button
                      type="button"
                      onClick={() => openConv(c)}
                      data-testid={`ask-conv-${c.id}`}
                      className={cn(
                        'flex w-full flex-col gap-0.5 rounded-md px-2.5 py-2 text-start transition-colors',
                        activeConv?.id === c.id
                          ? 'bg-primary/10 text-foreground'
                          : 'hover:bg-muted',
                      )}
                    >
                      <span className="flex items-center gap-1.5">
                        {c.scope === 'workspace'
                          ? <Building2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                          : <Globe className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />}
                        <span className="truncate text-sm">{c.title}</span>
                      </span>
                      <span className="ps-5 text-[11px] text-muted-foreground">
                        {c.updated_at ? formatRelativeTime(c.updated_at) : 'just now'}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </aside>

        {/* Chat pane */}
        <div className="flex min-h-0 flex-col rounded-lg border border-border bg-card lg:overflow-hidden">
          <div ref={threadRef} className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4" data-testid="ask-thread">
            {loadingConv ? (
              <div className="flex justify-center p-8"><Spinner /></div>
            ) : messages.length === 0 ? (
              <EmptyChat onPick={(q) => send(q)} disabled={streaming} />
            ) : (
              messages.map((m) => <MessageBubble key={m.id} m={m} />)
            )}
          </div>

          {/* Composer */}
          <div className="border-t border-border p-3">
            <div className="rounded-lg border border-border bg-background focus-within:ring-2 focus-within:ring-ring">
              <Textarea
                value={input}
                onChange={(e) => setInput(e.target.value.slice(0, MAX_QUESTION_LEN))}
                onKeyDown={(e) => {
                  if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') { e.preventDefault(); void send() }
                }}
                placeholder="Ask anything about your documents…"
                rows={2}
                className="resize-none border-0 bg-transparent focus-visible:ring-0"
                data-testid="ask-input"
              />
              <div className="flex items-center justify-between gap-2 px-2 pb-2">
                {/* Scope: fixed once a conversation exists; editable for new chats. */}
                <SelectRoot
                  value={activeConv
                    ? (activeConv.scope === 'workspace' ? (activeConv.workspace_id ?? ALL_WORKSPACES) : ALL_WORKSPACES)
                    : workspaceId}
                  onValueChange={(v) => { if (!activeConv) setWorkspaceId(v) }}
                  disabled={!!activeConv || streaming}
                >
                  <SelectTrigger className="h-8 w-auto gap-1.5 border-0 bg-muted/60 text-xs" data-testid="ask-scope">
                    <Globe className="h-3.5 w-3.5" />
                    <SelectValue>{scopeLabel}</SelectValue>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={ALL_WORKSPACES}>All workspaces</SelectItem>
                    {(workspaces.data ?? []).map((w) => (
                      <SelectItem key={w.id} value={w.id}>{w.name}</SelectItem>
                    ))}
                  </SelectContent>
                </SelectRoot>
                <div className="flex items-center gap-2">
                  <span className="text-[11px] tabular-nums text-muted-foreground">
                    {input.length}/{MAX_QUESTION_LEN}
                  </span>
                  <Button
                    size="sm"
                    onClick={() => void send()}
                    disabled={!input.trim() || streaming}
                    loading={streaming}
                    data-testid="ask-send"
                  >
                    <Send className="h-4 w-4" /> Ask
                  </Button>
                </div>
              </div>
            </div>
            <p className="mt-1.5 px-1 text-[11px] text-muted-foreground">
              <kbd className="rounded bg-muted px-1">⌘/Ctrl + Enter</kbd> to send · answers come from your readable documents only.
            </p>
          </div>
        </div>
      </div>
    </div>
  )
}

function EmptyChat({ onPick, disabled }: { onPick: (q: string) => void; disabled: boolean }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-5 py-10 text-center">
      <span className="flex h-14 w-14 items-center justify-center rounded-full bg-primary/10">
        <Sparkles className="h-7 w-7 text-primary" />
      </span>
      <div>
        <h2 className="text-lg font-semibold">Ask your documents</h2>
        <p className="mx-auto mt-1 max-w-md text-sm text-muted-foreground">
          Ask a question across your workspaces. Each answer is grounded in real document pages, and follow-ups keep the conversation's context.
        </p>
      </div>
      <div className="grid w-full max-w-lg gap-2 sm:grid-cols-2">
        {EXAMPLE_QUESTIONS.map((q) => (
          <button
            key={q}
            type="button"
            disabled={disabled}
            onClick={() => onPick(q)}
            className="flex items-start gap-2 rounded-lg border border-border p-3 text-start text-sm transition-colors hover:bg-muted disabled:opacity-50"
          >
            <Sparkles className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
            <span>{q}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

function MessageBubble({ m }: { m: UIMessage }) {
  if (m.role === 'user') {
    return (
      <div className="flex justify-end" data-testid="ask-msg-user">
        <div className="flex max-w-[80%] items-start gap-2">
          <div className="rounded-2xl rounded-tr-sm bg-primary px-3.5 py-2 text-sm text-primary-foreground">
            {m.content}
          </div>
          <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-muted">
            <UserIcon className="h-4 w-4 text-muted-foreground" />
          </span>
        </div>
      </div>
    )
  }
  return (
    <div className="flex justify-start" data-testid="ask-msg-assistant">
      <div className="flex max-w-[85%] items-start gap-2">
        <span className="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-primary/10">
          <Sparkles className="h-4 w-4 text-primary" />
        </span>
        <div className="min-w-0 rounded-2xl rounded-tl-sm border border-border bg-background px-3.5 py-2 text-sm">
          {m.content
            ? <AnswerMarkdown text={m.content} />
            : <span className="inline-flex items-center gap-2 text-muted-foreground"><Spinner className="h-3.5 w-3.5" /> Thinking…</span>}
          {m.citations && m.citations.length > 0 && <CitationChips citations={m.citations} />}
        </div>
      </div>
    </div>
  )
}

function CitationChips({ citations }: { citations: Citation[] }) {
  const groups = useMemo(() => {
    const map = new Map<string, { title: string; pages: number[]; n: number }>()
    let n = 0
    for (const c of citations) {
      let g = map.get(c.document_id)
      if (!g) { g = { title: c.document_title || 'Document', pages: [], n: ++n }; map.set(c.document_id, g) }
      if (c.document_title && (!g.title || g.title === 'Document')) g.title = c.document_title
      if (c.page != null && !g.pages.includes(c.page)) g.pages.push(c.page)
    }
    return [...map.entries()].map(([id, g]) => ({ id, ...g, pages: g.pages.sort((a, b) => a - b) }))
  }, [citations])
  if (!groups.length) return null
  return (
    <div className="mt-2 flex flex-wrap gap-1.5 border-t border-border pt-2">
      {groups.map((g) => (
        <span
          key={g.id}
          className="inline-flex max-w-full items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 text-[11px] text-muted-foreground"
          title={g.title}
        >
          <span className="font-semibold text-foreground">[{g.n}]</span>
          <span className="max-w-[220px] truncate">{g.title}</span>
          {g.pages.length > 0 && <span className="shrink-0">· p.{g.pages.join(', ')}</span>}
        </span>
      ))}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/ask')({ component: AskPage })
