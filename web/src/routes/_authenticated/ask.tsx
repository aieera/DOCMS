import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import toast from 'react-hot-toast'
import { Sparkles, ThumbsUp, ThumbsDown, Flag, Send } from 'lucide-react'

import { getWorkspaces } from '@/api/workspaces'
import { queryRAG, sendRAGFeedback, type RAGCitation, type RAGQueryResponse, type RAGFeedback } from '@/api/rag'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Textarea } from '@/components/ui/Textarea'
import { Select } from '@/components/ui/Select'
import { Spinner } from '@/components/ui/Spinner'
import { EmptyState } from '@/components/ui/EmptyState'

const ALL_WORKSPACES = '__all__'

function AskPage() {
  const [question, setQuestion] = useState('')
  const [workspaceId, setWorkspaceId] = useState<string>(ALL_WORKSPACES)
  const [answer, setAnswer] = useState<RAGQueryResponse | null>(null)
  const [feedback, setFeedback] = useState<RAGFeedback | null>(null)

  const { data: workspaces } = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })

  const askMut = useMutation({
    mutationFn: queryRAG,
    onSuccess: (data) => {
      setAnswer(data)
      setFeedback(null)
    },
    onError: () => {
      toast.error('Could not get an answer — please retry')
    },
  })

  const feedbackMut = useMutation({
    mutationFn: ({ id, kind }: { id: string; kind: RAGFeedback }) => sendRAGFeedback(id, kind),
    onSuccess: (_d, vars) => {
      setFeedback(vars.kind)
      toast.success('Thanks for the feedback')
    },
    onError: () => toast.error('Failed to record feedback'),
  })

  const handleSubmit = () => {
    const q = question.trim()
    if (!q) return
    askMut.mutate({
      question: q,
      workspaceId: workspaceId === ALL_WORKSPACES ? undefined : workspaceId,
    })
  }

  const wsOptions = [
    { value: ALL_WORKSPACES, label: 'All workspaces' },
    ...(workspaces ?? []).map((w) => ({ value: w.id, label: w.name })),
  ]

  return (
    <div>
      <PageHeader
        title="Ask"
        description="Ask a question across your documents — answers cite the source pages."
      />

      <div className="space-y-3">
        <div className="flex items-end gap-2">
          <div className="w-64">
            <Select
              label="Scope"
              value={workspaceId}
              onValueChange={setWorkspaceId}
              options={wsOptions}
            />
          </div>
          <div className="flex-1">
            <Textarea
              placeholder="Ask anything about your documents…"
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              rows={3}
              data-testid="ask-question-input"
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  handleSubmit()
                }
              }}
            />
          </div>
          <Button
            onClick={handleSubmit}
            disabled={!question.trim() || askMut.isPending}
            data-testid="ask-submit"
          >
            {askMut.isPending ? <Spinner className="h-4 w-4" /> : <Send className="h-4 w-4" />}
            Ask
          </Button>
        </div>
        <p className="text-xs text-muted-foreground">
          ⌘/Ctrl + Enter to submit. Answers come from your readable documents only.
        </p>
      </div>

      {askMut.isPending && (
        <div className="mt-6 flex items-center gap-2 text-muted-foreground">
          <Spinner className="h-4 w-4" /> Searching your documents…
        </div>
      )}

      {answer && !askMut.isPending && (
        <div className="mt-6 space-y-4" data-testid="ask-answer">
          <AnswerCard
            answer={answer}
            feedback={feedback}
            onFeedback={(kind) => feedbackMut.mutate({ id: answer.query_id, kind })}
            feedbackPending={feedbackMut.isPending}
          />
          <CitationsList citations={answer.citations} />
        </div>
      )}

      {!answer && !askMut.isPending && (
        <div className="mt-12">
          <EmptyState
            icon={<Sparkles className="h-8 w-8" />}
            title="Ask your documents"
            description="Type a question above to search across your workspaces. Each answer is grounded in real document pages."
          />
        </div>
      )}
    </div>
  )
}

function AnswerCard(props: {
  answer: RAGQueryResponse
  feedback: RAGFeedback | null
  onFeedback: (kind: RAGFeedback) => void
  feedbackPending: boolean
}) {
  const { answer, feedback, onFeedback, feedbackPending } = props
  const isUnknown = answer.answer.trim() ==="I don't know."

  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="prose prose-sm max-w-none whitespace-pre-wrap text-foreground">
        {renderAnswerWithInlineCitations(answer.answer, answer.citations)}
      </div>

      <div className="mt-3 flex items-center justify-between border-t border-border pt-3 text-xs text-muted-foreground">
        <span>
          {answer.model || 'unknown model'} · {answer.elapsed_ms}ms · {answer.input_tokens + answer.output_tokens} tokens
        </span>
        {!isUnknown && (
          <div className="flex items-center gap-1">
            <FeedbackButton
              icon={<ThumbsUp className="h-3.5 w-3.5" />}
              active={feedback === 'up'}
              disabled={feedbackPending || feedback !== null}
              onClick={() => onFeedback('up')}
              testId="ask-feedback-up"
              label="Helpful"
            />
            <FeedbackButton
              icon={<ThumbsDown className="h-3.5 w-3.5" />}
              active={feedback === 'down'}
              disabled={feedbackPending || feedback !== null}
              onClick={() => onFeedback('down')}
              testId="ask-feedback-down"
              label="Not helpful"
            />
            <FeedbackButton
              icon={<Flag className="h-3.5 w-3.5" />}
              active={feedback === 'flag'}
              disabled={feedbackPending || feedback !== null}
              onClick={() => onFeedback('flag')}
              testId="ask-feedback-flag"
              label="Flag"
            />
          </div>
        )}
      </div>
    </div>
  )
}

function FeedbackButton(props: {
  icon: React.ReactNode
  active: boolean
  disabled: boolean
  onClick: () => void
  testId: string
  label: string
}) {
  return (
    <button
      type="button"
      onClick={props.onClick}
      disabled={props.disabled}
      data-testid={props.testId}
      aria-label={props.label}
      title={props.label}
      className={`flex h-7 items-center gap-1 rounded px-2 transition-colors ${
        props.active
          ? 'bg-primary/10 text-primary'
          : 'hover:bg-muted'
      } disabled:opacity-50`}
    >
      {props.icon}
    </button>
  )
}

// Replace [doc_id:page_X] tokens in the answer text with clickable
// links to the underlying document page. Falls back to plain text
// when the cite doesn't match any returned citation (rare; means the
// LLM hallucinated a doc id, in which case we don't want a broken
// link).
function renderAnswerWithInlineCitations(text: string, citations: RAGCitation[]) {
  // Build {doc_id|doc_id:page_N -> citation} for fast match.
  const byKey = new Map<string, RAGCitation>()
  for (const c of citations) {
    byKey.set(c.doc_id, c)
    if (c.page != null) byKey.set(`${c.doc_id}:page_${c.page}`, c)
  }
  const parts: React.ReactNode[] = []
  const re = /\[([0-9a-f-]{8,}(?::page_\d+)?)\]/gi
  let lastIdx = 0
  let m: RegExpExecArray | null
  let key = 0
  while ((m = re.exec(text)) !== null) {
    if (m.index > lastIdx) parts.push(text.slice(lastIdx, m.index))
    const token = m[1]
    const cite = byKey.get(token)
    if (cite && cite.workspace_id) {
      const display = cite.page != null
        ? `[${shortId(cite.doc_id)}:p${cite.page}]`
        : `[${shortId(cite.doc_id)}]`
      parts.push(
        <Link
          key={`cite-${key++}`}
          to="/workspaces/$workspaceId/documents/$documentId"
          params={{ workspaceId: cite.workspace_id, documentId: cite.doc_id }}
          className="text-primary underline-offset-2 hover:underline"
          data-testid="ask-inline-citation"
          title={cite.snippet}
        >
          {display}
        </Link>,
      )
    } else {
      // Either an unmatched citation token or one whose chunk pre-dates
      // the workspace_id-on-payload change — fall back to plain text
      // rather than rendering a broken link.
      parts.push(m[0])
    }
    lastIdx = m.index + m[0].length
  }
  if (lastIdx < text.length) parts.push(text.slice(lastIdx))
  return parts
}

function shortId(id: string) {
  return id.length > 8 ? id.slice(0, 8) : id
}

function CitationsList({ citations }: { citations: RAGCitation[] }) {
  if (!citations.length) return null
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <h3 className="mb-3 text-sm font-medium">Sources</h3>
      <ul className="space-y-3" data-testid="ask-citations-list">
        {citations.map((c, i) => (
          <li key={`${c.doc_id}-${c.chunk_id}-${i}`} className="flex flex-col gap-1 text-sm">
            {c.workspace_id ? (
              <Link
                to="/workspaces/$workspaceId/documents/$documentId"
                params={{ workspaceId: c.workspace_id, documentId: c.doc_id }}
                className="font-medium text-primary hover:underline"
              >
                {shortId(c.doc_id)}
                {c.page != null && <span> · page {c.page}</span>}
                {c.section_path && (
                  <span className="ml-1 text-xs font-normal text-muted-foreground">
                    · {c.section_path}
                  </span>
                )}
              </Link>
            ) : (
              <span className="font-medium">
                {shortId(c.doc_id)}
                {c.page != null && <span> · page {c.page}</span>}
              </span>
            )}
            <p className="text-xs text-muted-foreground">{c.snippet}</p>
          </li>
        ))}
      </ul>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/ask')({ component: AskPage })
