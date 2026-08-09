import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { History, MessageSquare, Send, Sparkles, Square } from 'lucide-react'

import {
  getQAHistory,
  streamQA,
  type Citation,
  type QAConversation,
  type QAMessage,
  type QAStreamEvent,
} from '@/api/doc-qa'
import { Button } from '@/components/ui/shadcn/button'
import { AnswerMarkdown } from '@/components/ai/AnswerMarkdown'
import { CitationList } from './CitationHighlight'
import { SuggestedQuestions } from './SuggestedQuestions'
import { formatUsd } from '@/lib/formatters'

interface Props {
  documentId: string
  /** Optional: lift citation clicks up to a PDF viewer that knows
   * how to scroll + highlight. */
  onJumpToCitation?: (c: Citation) => void
}

interface UIMessage {
  role: 'user' | 'assistant'
  content: string
  citations?: Citation[]
  pending?: boolean
  // Filled from the SSE 'done' event so the bubble can show
  // tokens/cost/latency under the answer.
  usage?: {
    model: string
    input_tokens: number
    output_tokens: number
    cost_usd: number
    elapsed_ms: number
  }
}

export function DocQAChat({ documentId, onJumpToCitation }: Props) {
  const qc = useQueryClient()
  const [input, setInput] = useState('')
  const [conversationId, setConversationId] = useState<string | undefined>()
  const [messages, setMessages] = useState<UIMessage[]>([])
  const [streaming, setStreaming] = useState(false)
  const [showHistory, setShowHistory] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)

  const { data: history } = useQuery({
    queryKey: ['qa-history', documentId],
    queryFn: () => getQAHistory(documentId),
    enabled: showHistory,
  })

  // Auto-scroll to latest message when content changes.
  useEffect(() => {
    scrollRef.current?.scrollTo({
      top: scrollRef.current.scrollHeight,
      behavior: 'smooth',
    })
  }, [messages])

  const submit = async (questionRaw?: string) => {
    const question = (questionRaw ?? input).trim()
    if (!question || streaming) return
    setInput('')
    setMessages((m) => [
      ...m,
      { role: 'user', content: question },
      { role: 'assistant', content: '', pending: true },
    ])
    setStreaming(true)

    const ctrl = new AbortController()
    abortRef.current = ctrl
    try {
      await streamQA({
        documentId,
        question,
        conversationId,
        signal: ctrl.signal,
        onEvent: (evt: QAStreamEvent) => {
          if (evt.type === 'conversation') {
            setConversationId(evt.conversation_id)
            return
          }
          if (evt.type === 'citations') {
            setMessages((m) => updateLastAssistant(m, (a) => ({
              ...a,
              citations: evt.citations,
            })))
            return
          }
          if (evt.type === 'chunk') {
            setMessages((m) => updateLastAssistant(m, (a) => ({
              ...a,
              content: a.content + evt.text,
              pending: true,
            })))
            return
          }
          if (evt.type === 'done') {
            setMessages((m) => updateLastAssistant(m, (a) => ({
              ...a,
              content: evt.full_text || a.content,
              citations: evt.citations,
              pending: false,
              usage: {
                model: evt.model,
                input_tokens: evt.input_tokens ?? 0,
                output_tokens: evt.output_tokens ?? 0,
                cost_usd: evt.cost_usd ?? 0,
                elapsed_ms: evt.elapsed_ms ?? 0,
              },
            })))
          } else if (evt.type === 'error') {
            // Keep whatever streamed before the failure, but say so —
            // a silently truncated answer reads as a complete (wrong)
            // one.
            setMessages((m) => updateLastAssistant(m, (a) => ({
              ...a,
              content: a.content
                ? `${a.content}\n\n> ⚠️ The answer was cut off (${evt.message}). Try asking again.`
                : `Error: ${evt.message}`,
              pending: false,
            })))
          }
        },
      })
    } catch (e) {
      const msg = e instanceof Error ? e.message : 'Request failed'
      setMessages((m) => updateLastAssistant(m, (a) => ({
        ...a, content: a.content || `Error: ${msg}`, pending: false,
      })))
    } finally {
      setStreaming(false)
      abortRef.current = null
      qc.invalidateQueries({ queryKey: ['qa-history', documentId] })
    }
  }

  const cancel = () => {
    abortRef.current?.abort()
    abortRef.current = null
  }

  const newConversation = () => {
    cancel()
    setConversationId(undefined)
    setMessages([])
    setShowHistory(false)
  }

  const loadConversation = async (conv: QAConversation) => {
    cancel()
    setConversationId(conv.id)
    setShowHistory(false)
    const data = await getQAHistory(documentId, conv.id)
    if (!data.messages) {
      setMessages([])
      return
    }
    setMessages(
      data.messages.map((m: QAMessage) => ({
        role: m.role,
        content: m.content,
        citations: m.citations,
      })),
    )
  }

  return (
    <div className="flex h-[600px] flex-col rounded-2xl border border-border bg-card">
      <div className="flex items-center justify-between border-b border-border px-4 py-2 text-sm font-medium">
        <div className="flex items-center gap-2">
          <MessageSquare className="h-4 w-4 text-primary" />
          Ask about this document
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => setShowHistory((v) => !v)}>
            <History className="me-1 h-4 w-4" />
            History
          </Button>
          <Button size="sm" variant="ghost" onClick={newConversation}>
            <Sparkles className="me-1 h-4 w-4" />
            New
          </Button>
        </div>
      </div>

      {showHistory && (
        <div className="border-b border-border px-4 py-2 text-xs">
          <div className="mb-1 text-muted-foreground">Previous conversations</div>
          {(history?.conversations ?? []).length === 0 ? (
            <div className="text-muted-foreground">No history yet.</div>
          ) : (
            <ul className="space-y-1">
              {history!.conversations.map((c) => (
                <li key={c.id}>
                  <button
                    onClick={() => loadConversation(c)}
                    className="block w-full truncate rounded-md px-2 py-1 text-start hover:bg-muted"
                  >
                    <span className="text-foreground">{c.title}</span>
                    <span className="ms-2 text-muted-foreground">
                      {new Date(c.updated_at).toLocaleString()}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div ref={scrollRef} className="flex-1 space-y-3 overflow-y-auto px-4 py-3">
        {messages.length === 0 && !streaming && (
          <div className="text-sm text-muted-foreground">
            Ask a question to start. Answers cite the document.
          </div>
        )}
        {messages.map((m, i) => (
          <Bubble
            key={i}
            message={m}
            onJumpToCitation={onJumpToCitation}
          />
        ))}
      </div>

      {messages.length === 0 && (
        <SuggestedQuestions onPick={(q) => submit(q)} />
      )}

      <div className="flex items-end gap-2 border-t border-border bg-background/40 px-3 py-3">
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault()
              submit()
            }
          }}
          placeholder="Ask a question…"
          rows={1}
          className="flex-1 resize-none rounded-xl border border-input bg-background px-3 py-2 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1 focus:ring-offset-background"
        />
        {streaming ? (
          <Button size="sm" variant="outline" onClick={cancel} aria-label="Stop">
            <Square className="h-4 w-4" />
          </Button>
        ) : (
          <Button size="sm" disabled={!input.trim()} onClick={() => submit()} aria-label="Send">
            <Send className="h-4 w-4" />
          </Button>
        )}
      </div>
    </div>
  )
}

function Bubble({
  message,
  onJumpToCitation,
}: {
  message: UIMessage
  onJumpToCitation?: (c: Citation) => void
}) {
  const isUser = message.role === 'user'
  return (
    <div className={['flex', isUser ? 'justify-end' : 'justify-start'].join(' ')}>
      <div
        className={[
          'max-w-[85%] rounded-2xl px-3 py-2 text-sm shadow-sm',
          isUser
            ? 'bg-primary text-primary-foreground'
            : 'bg-muted text-foreground',
        ].join(' ')}
      >
        {!isUser && message.content ? (
          // Assistant answers arrive as Markdown — render it instead of
          // showing raw `**bold**` / `-` syntax. User bubbles stay plain.
          <AnswerMarkdown
            text={message.content}
            className="prose prose-sm max-w-none dark:prose-invert"
          />
        ) : (
          <div className="whitespace-pre-wrap">
            {message.content || (message.pending ? <TypingDots /> : '')}
          </div>
        )}
        {!isUser && message.citations && message.citations.length > 0 && (
          <CitationList
            citations={message.citations}
            onJumpTo={onJumpToCitation}
          />
        )}
        {!isUser && message.usage && !message.pending && (
          <UsageChip usage={message.usage} />
        )}
      </div>
    </div>
  )
}

function UsageChip({ usage }: { usage: NonNullable<UIMessage['usage']> }) {
  const cost = formatUsd(usage.cost_usd)
  const seconds = (usage.elapsed_ms / 1000).toFixed(1)
  return (
    <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-0.5 border-t border-border/60 pt-1.5 text-[10px] text-muted-foreground">
      <span className="font-mono">{usage.model}</span>
      <span>·</span>
      <span title={`${usage.input_tokens.toLocaleString()} input + ${usage.output_tokens.toLocaleString()} output`}>
        {usage.input_tokens.toLocaleString()} → {usage.output_tokens.toLocaleString()} tok
      </span>
      <span>·</span>
      <span>{cost}</span>
      <span>·</span>
      <span>{seconds}s</span>
    </div>
  )
}

function TypingDots() {
  return (
    <span className="inline-flex gap-1">
      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current" />
      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current [animation-delay:120ms]" />
      <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-current [animation-delay:240ms]" />
    </span>
  )
}

function updateLastAssistant(
  msgs: UIMessage[],
  fn: (m: UIMessage) => UIMessage,
): UIMessage[] {
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') {
      const next = [...msgs]
      next[i] = fn(msgs[i])
      return next
    }
  }
  return msgs
}
