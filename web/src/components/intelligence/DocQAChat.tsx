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
import { Button } from '@/components/ui/Button'
import { CitationList } from './CitationHighlight'
import { SuggestedQuestions } from './SuggestedQuestions'

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
            })))
          } else if (evt.type === 'error') {
            setMessages((m) => updateLastAssistant(m, (a) => ({
              ...a,
              content: a.content || `Error: ${evt.message}`,
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
    <div className="flex h-[600px] flex-col rounded border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center justify-between border-b border-zinc-100 px-4 py-2 text-sm font-medium dark:border-zinc-900">
        <div className="flex items-center gap-2">
          <MessageSquare className="h-4 w-4 text-violet-500" />
          Ask about this document
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="ghost" onClick={() => setShowHistory((v) => !v)}>
            <History className="mr-1 h-4 w-4" />
            History
          </Button>
          <Button size="sm" variant="ghost" onClick={newConversation}>
            <Sparkles className="mr-1 h-4 w-4" />
            New
          </Button>
        </div>
      </div>

      {showHistory && (
        <div className="border-b border-zinc-100 px-4 py-2 text-xs dark:border-zinc-900">
          <div className="mb-1 text-zinc-500">Previous conversations</div>
          {(history?.conversations ?? []).length === 0 ? (
            <div className="text-zinc-400">No history yet.</div>
          ) : (
            <ul className="space-y-1">
              {history!.conversations.map((c) => (
                <li key={c.id}>
                  <button
                    onClick={() => loadConversation(c)}
                    className="block w-full truncate rounded px-2 py-1 text-left hover:bg-zinc-100 dark:hover:bg-zinc-800"
                  >
                    <span className="text-zinc-700 dark:text-zinc-200">{c.title}</span>
                    <span className="ml-2 text-zinc-400">
                      {new Date(c.updated_at).toLocaleString()}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div ref={scrollRef} className="flex-1 overflow-y-auto px-4 py-3 space-y-3">
        {messages.length === 0 && !streaming && (
          <div className="text-sm text-zinc-500">
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

      <div className="flex items-end gap-2 border-t border-zinc-100 px-3 py-3 dark:border-zinc-900">
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
          className="flex-1 resize-none rounded border border-zinc-200 bg-white px-3 py-2 text-sm focus:outline-none focus:ring-1 focus:ring-violet-500 dark:border-zinc-800 dark:bg-zinc-900"
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
          'max-w-[85%] rounded-lg px-3 py-2 text-sm',
          isUser
            ? 'bg-violet-500 text-white'
            : 'bg-zinc-100 text-zinc-900 dark:bg-zinc-800 dark:text-zinc-100',
        ].join(' ')}
      >
        <div className="whitespace-pre-wrap">
          {message.content || (message.pending ? <TypingDots /> : '')}
        </div>
        {!isUser && message.citations && message.citations.length > 0 && (
          <CitationList
            citations={message.citations}
            onJumpTo={onJumpToCitation}
          />
        )}
      </div>
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
