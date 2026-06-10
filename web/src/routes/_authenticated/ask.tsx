import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useEffect, useMemo, useRef, useState } from 'react'
import { toast } from 'sonner'
import {
  Sparkles, ThumbsUp, ThumbsDown, Flag, Send, AlertTriangle,
  Copy, Check, X, Clock, Layers,
} from 'lucide-react'

import { getWorkspaces } from '@/api/workspaces'
import { queryRAG, sendRAGFeedback, type RAGCitation, type RAGQueryResponse, type RAGFeedback } from '@/api/rag'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Textarea } from '@/components/ui/shadcn/textarea'
import {
  Select as SelectRoot, SelectTrigger, SelectValue, SelectContent, SelectItem,
} from '@/components/ui/shadcn/select'
import { Spinner } from '@/components/ui/Spinner'
import { Tooltip, TooltipContent, TooltipTrigger, TooltipProvider } from '@/components/ui/shadcn/tooltip'

const ALL_WORKSPACES = '__all__'
// Mirrors the backend cap in services/intelligence/app/api/routes.py.
const MAX_QUESTION_LEN = 4000
const RECENT_KEY = 'ask:recent-questions'
const MAX_RECENT = 6
const UNKNOWN_ANSWER = "I don't know."

const EXAMPLE_QUESTIONS = [
  'What documents are available?',
  'Summarize the key terms of the latest contract.',
  'What are the main obligations in the partner program?',
  'Which documents mention renewal or termination?',
]

function loadRecent(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((x) => typeof x === 'string').slice(0, MAX_RECENT) : []
  } catch {
    return []
  }
}

// One source = one document. The backend can return several chunks from
// the same document; we group them so the sources list shows each
// document once (with its cited pages) instead of duplicate rows, and so
// the inline [N] index is stable per document.
interface CitationGroup {
  n: number
  doc_id: string
  title: string
  workspace_id: string | null
  bestScore: number
  pages: { page: number | null; snippet: string; score: number; chunk_id: number | null }[]
}

function groupCitations(citations: RAGCitation[]): CitationGroup[] {
  const order: string[] = []
  // _doc / _section accumulate the best title candidates across ALL of a
  // document's chunks — some chunks may carry document_title/section_path
  // while others don't, so picking only the first chunk's value showed
  // "Untitled document" even when a sibling chunk had the real title.
  const map = new Map<string, CitationGroup & { _doc?: string; _section?: string }>()
  let next = 1
  for (const c of citations) {
    let g = map.get(c.doc_id)
    if (!g) {
      g = {
        n: next++,
        doc_id: c.doc_id,
        title: 'Untitled document',
        workspace_id: c.workspace_id,
        bestScore: c.score,
        pages: [],
      }
      map.set(c.doc_id, g)
      order.push(c.doc_id)
    }
    if (c.document_title && !g._doc) g._doc = c.document_title
    if (c.section_path && !g._section) g._section = c.section_path
    g.bestScore = Math.max(g.bestScore, c.score)
    if (!g.pages.some((p) => p.page === c.page)) {
      g.pages.push({ page: c.page, snippet: c.snippet, score: c.score, chunk_id: c.chunk_id })
    }
  }
  for (const g of map.values()) {
    g.title = g._doc ?? g._section ?? 'Untitled document'
    g.pages.sort((a, b) => (a.page ?? 0) - (b.page ?? 0))
  }
  return order.map((id) => map.get(id)!)
}

// Chunking can begin a snippet mid-word ("olutions to customers…") and
// mid-sentence. Drop a leading partial token, strip leading punctuation,
// and signal truncation with ellipses so passages read cleanly.
function cleanSnippet(s: string | undefined): string {
  let t = (s ?? '').trim()
  if (!t) return ''
  let cut = false
  if (/^[a-z]/.test(t)) {
    const sp = t.indexOf(' ')
    if (sp > 0 && sp <= 24) { t = t.slice(sp + 1); cut = true }
  }
  t = t.replace(/^[\s,;:.)]+/, '')
  const needTail = t.length > 0 && !/[.!?]['")\]]?$/.test(t)
  return `${cut ? '… ' : ''}${t}${needTail ? '…' : ''}`
}

// rerank/cosine scores aren't guaranteed 0..1; render a percentage when
// they look normalised, otherwise a 2-dp value. Purely informational.
function formatRelevance(score: number): string {
  if (score >= 0 && score <= 1) return `${Math.round(score * 100)}%`
  return score.toFixed(2)
}

function AskPage() {
  const [question, setQuestion] = useState('')
  const [workspaceId, setWorkspaceId] = useState<string>(ALL_WORKSPACES)
  const [answer, setAnswer] = useState<RAGQueryResponse | null>(null)
  const [feedback, setFeedback] = useState<RAGFeedback | null>(null)
  const [recent, setRecent] = useState<string[]>(() => loadRecent())
  const taRef = useRef<HTMLTextAreaElement>(null)
  // The question that produced the currently-shown answer, so re-submitting
  // the identical text gives a visible "re-running" cue instead of silently
  // swapping in a fresh answer.
  const lastAskedRef = useRef<string | null>(null)

  const { data: workspaces, isError: workspacesError } = useQuery({ queryKey: ['workspaces'], queryFn: getWorkspaces })

  const askMut = useAppMutation({
    mutationFn: queryRAG,
    onSuccess: (data) => {
      setAnswer(data)
      setFeedback(null)
    },
    onError: () => {
      toast.error('Could not get an answer — please retry')
    },
  })

  const feedbackMut = useAppMutation({
    mutationFn: ({ id, kind }: { id: string; kind: RAGFeedback }) => sendRAGFeedback(id, kind),
    onSuccess: (_d, vars) => {
      setFeedback(vars.kind)
      toast.success('Thanks for the feedback')
    },
    onError: () => toast.error('Failed to record feedback'),
  })

  // Auto-grow the textarea up to a cap so long questions are visible
  // without an inner scrollbar, then scroll.
  useEffect(() => {
    const el = taRef.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 240)}px`
  }, [question])

  const pushRecent = (q: string) => {
    setRecent((prev) => {
      const next = [q, ...prev.filter((x) => x !== q)].slice(0, MAX_RECENT)
      try { localStorage.setItem(RECENT_KEY, JSON.stringify(next)) } catch { /* ignore quota */ }
      return next
    })
  }

  const runQuery = (qText: string) => {
    const q = qText.trim()
    if (!q || askMut.isPending) return
    const isRepeat = q === lastAskedRef.current && !!answer
    lastAskedRef.current = q
    pushRecent(q)
    if (isRepeat) toast.message('Re-running the same question…')
    askMut.mutate({
      question: q.slice(0, MAX_QUESTION_LEN),
      workspaceId: workspaceId === ALL_WORKSPACES ? undefined : workspaceId,
    })
  }

  const handleSubmit = () => runQuery(question)

  const askExample = (q: string) => {
    setQuestion(q)
    runQuery(q)
  }

  const wsOptions = [
    { value: ALL_WORKSPACES, label: 'All workspaces' },
    ...(workspaces ?? []).map((w) => ({ value: w.id, label: w.name })),
  ]

  const overLimit = question.length > MAX_QUESTION_LEN

  return (
    <TooltipProvider delayDuration={200}>
      <div className="mx-auto max-w-3xl">
        <PageHeader
          title="Ask"
          description="Ask a question across your documents — answers cite the source pages."
        />

        <div className="space-y-3">
          {/* Unified composer: textarea on top, a footer bar holding the
              scope picker (left) and Ask button (right). One bordered
              surface that lights up on focus, instead of three
              mismatched-height controls in a row. */}
          <div className="rounded-2xl border border-border bg-card shadow-sm transition focus-within:border-primary/50 focus-within:ring-1 focus-within:ring-primary/40">
            <div className="relative">
              <Textarea
                ref={taRef}
                aria-label="Ask a question about your documents"
                placeholder="Ask anything about your documents…"
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                rows={3}
                maxLength={MAX_QUESTION_LEN}
                className="resize-none border-0 bg-transparent px-4 pt-3.5 pe-10 text-[15px] shadow-none focus-visible:ring-0"
                data-testid="ask-question-input"
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                    e.preventDefault()
                    handleSubmit()
                  }
                }}
              />
              {question && (
                <button
                  type="button"
                  onClick={() => { setQuestion(''); taRef.current?.focus() }}
                  aria-label="Clear question"
                  title="Clear"
                  className="absolute end-2.5 top-2.5 rounded p-0.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <X className="h-4 w-4" />
                </button>
              )}
            </div>

            <div className="flex items-center justify-between gap-2 px-2.5 pb-2.5 pt-1">
              <SelectRoot value={workspaceId} onValueChange={setWorkspaceId}>
                <SelectTrigger
                  aria-label="Scope"
                  className="h-8 w-auto gap-1.5 border-0 bg-transparent px-2 text-muted-foreground shadow-none hover:bg-muted hover:text-foreground focus:ring-0 data-[state=open]:bg-muted"
                >
                  <Layers className="h-3.5 w-3.5 shrink-0 opacity-70" aria-hidden />
                  <SelectValue placeholder="All workspaces" />
                </SelectTrigger>
                <SelectContent>
                  {wsOptions.map((o) => (
                    <SelectItem key={o.value} value={o.value}>{o.label}</SelectItem>
                  ))}
                </SelectContent>
              </SelectRoot>

              <div className="flex items-center gap-2.5">
                <span
                  className={`hidden text-xs tabular-nums sm:inline ${overLimit ? 'font-medium text-destructive' : 'text-muted-foreground'}`}
                  aria-live="polite"
                >
                  {question.length.toLocaleString()}/{MAX_QUESTION_LEN.toLocaleString()}
                </span>
                <Button
                  onClick={handleSubmit}
                  disabled={!question.trim() || overLimit || askMut.isPending}
                  data-testid="ask-submit"
                  aria-label="Ask the corpus"
                  title="Ask the corpus (⌘/Ctrl + Enter)"
                  size="sm"
                  className="gap-1.5 rounded-lg"
                >
                  {askMut.isPending ? <Spinner className="h-4 w-4" /> : <Send className="h-4 w-4" />}
                  <span>Ask</span>
                </Button>
              </div>
            </div>
          </div>

          <div className="flex items-center justify-between gap-2 px-1 text-xs text-muted-foreground">
            <p>
              <kbd className="me-1 inline-flex h-4 items-center rounded border border-border bg-muted px-1 font-mono text-[10px]">⌘/Ctrl + Enter</kbd>
              to submit. Answers come from your readable documents only.
            </p>
            {workspacesError && (
              <span className="flex items-center gap-1 text-destructive" role="alert">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden />
                Scoped search unavailable
              </span>
            )}
          </div>

          {recent.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="flex items-center gap-1 text-xs text-muted-foreground">
                <Clock className="h-3 w-3" aria-hidden /> Recent
              </span>
              {recent.map((q) => (
                <button
                  key={q}
                  type="button"
                  onClick={() => askExample(q)}
                  className="max-w-[16rem] truncate rounded-full border border-border bg-card px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:border-primary hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  title={q}
                >
                  {q}
                </button>
              ))}
            </div>
          )}
        </div>

        {askMut.isPending && <AnswerSkeleton />}

        {answer && !askMut.isPending && (
          <div className="mt-6 space-y-4" data-testid="ask-answer">
            <AnswerView
              answer={answer}
              feedback={feedback}
              onFeedback={(kind) => feedbackMut.mutate({ id: answer.query_id, kind })}
              feedbackPending={feedbackMut.isPending}
            />
          </div>
        )}

        {!answer && !askMut.isPending && (
          <div className="mt-12 flex flex-col items-center gap-3 text-center">
            <span className="flex h-16 w-16 items-center justify-center rounded-full bg-primary/10 text-primary">
              <Sparkles className="h-8 w-8" />
            </span>
            <h2 className="text-xl font-medium">Ask your documents</h2>
            <p className="max-w-md text-sm text-muted-foreground">
              Type a question above to search across your workspaces. Each answer is grounded in real document pages.
            </p>
            <div className="mt-3 grid w-full max-w-xl grid-cols-1 gap-2 sm:grid-cols-2">
              {EXAMPLE_QUESTIONS.map((q) => (
                <button
                  key={q}
                  type="button"
                  onClick={() => askExample(q)}
                  data-testid="ask-example"
                  className="group flex items-center gap-2.5 rounded-xl border border-border bg-card px-3.5 py-2.5 text-start text-sm text-muted-foreground transition-colors hover:border-primary/60 hover:bg-primary/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Sparkles className="h-3.5 w-3.5 shrink-0 text-primary/70" aria-hidden />
                  <span>{q}</span>
                </button>
              ))}
            </div>
          </div>
        )}
      </div>
    </TooltipProvider>
  )
}

function AnswerView(props: {
  answer: RAGQueryResponse
  feedback: RAGFeedback | null
  onFeedback: (kind: RAGFeedback) => void
  feedbackPending: boolean
}) {
  const { answer, feedback, onFeedback, feedbackPending } = props
  const [copied, setCopied] = useState(false)
  const groups = useMemo(() => groupCitations(answer.citations), [answer.citations])
  const byDoc = useMemo(() => new Map(groups.map((g) => [g.doc_id, g])), [groups])
  const isUnknown = answer.answer.trim() === UNKNOWN_ANSWER
  const hasSources = groups.length > 0

  const copyAnswer = async () => {
    try {
      await navigator.clipboard.writeText(buildCopyText(answer, groups))
      setCopied(true)
      toast.success('Answer copied')
      setTimeout(() => setCopied(false), 1500)
    } catch {
      toast.error('Copy failed')
    }
  }

  return (
    <>
      <div className="rounded-lg border border-border bg-card p-4">
        <div className="flex items-start justify-between gap-2">
          {isUnknown ? (
            <div className="text-sm text-foreground">
              {hasSources ? (
                <p className="flex items-start gap-2 text-muted-foreground">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" aria-hidden />
                  <span>I couldn&apos;t find a direct answer in your documents. The closest passages I found are listed below — try rephrasing or narrowing the scope.</span>
                </p>
              ) : (
                <p className="flex items-start gap-2 text-muted-foreground">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-500" aria-hidden />
                  <span>I couldn&apos;t find anything relevant in your readable documents. Try rephrasing the question, or widen the scope to “All workspaces”.</span>
                </p>
              )}
            </div>
          ) : (
            <div className="prose prose-sm max-w-none whitespace-pre-wrap text-foreground">
              {renderAnswerWithInlineCitations(answer.answer, byDoc)}
            </div>
          )}
          {!isUnknown && (
            <button
              type="button"
              onClick={copyAnswer}
              aria-label="Copy answer"
              title="Copy answer"
              className="shrink-0 rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="ask-copy"
            >
              {copied ? <Check className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
            </button>
          )}
        </div>

        <div className="mt-3 flex items-center justify-between gap-2 border-t border-border pt-3">
          {/* Item: technical metadata is opt-in, not always-on. */}
          <details className="text-xs text-muted-foreground">
            <summary className="cursor-pointer select-none rounded focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              Details
            </summary>
            <p className="mt-1 font-mono">
              {answer.model || 'unknown model'} · {answer.elapsed_ms}ms · {answer.input_tokens + answer.output_tokens} tokens
            </p>
          </details>
          {!isUnknown && (
            feedback !== null ? (
              <span
                className="flex items-center gap-1.5 text-xs font-medium text-green-600"
                data-testid="ask-feedback-confirm"
                role="status"
              >
                <Check className="h-3.5 w-3.5" aria-hidden />
                {feedback === 'flag' ? 'Flagged — thanks' : 'Thanks for your feedback'}
              </span>
            ) : (
              <div className="flex items-center gap-1">
                <FeedbackButton
                  icon={<ThumbsUp className="h-3.5 w-3.5" />}
                  active={feedback === 'up'}
                  disabled={feedbackPending}
                  onClick={() => onFeedback('up')}
                  testId="ask-feedback-up"
                  label="Helpful"
                />
                <FeedbackButton
                  icon={<ThumbsDown className="h-3.5 w-3.5" />}
                  active={feedback === 'down'}
                  disabled={feedbackPending}
                  onClick={() => onFeedback('down')}
                  testId="ask-feedback-down"
                  label="Not helpful"
                />
                <FeedbackButton
                  icon={<Flag className="h-3.5 w-3.5" />}
                  active={feedback === 'flag'}
                  disabled={feedbackPending}
                  onClick={() => onFeedback('flag')}
                  testId="ask-feedback-flag"
                  label="Flag"
                />
              </div>
            )
          )}
        </div>
      </div>

      <CitationsList groups={groups} heading={isUnknown ? 'Closest passages' : 'Sources'} />
    </>
  )
}

function buildCopyText(answer: RAGQueryResponse, groups: CitationGroup[]): string {
  const lines = [answer.answer.trim()]
  if (groups.length) {
    lines.push('', 'Sources:')
    for (const g of groups) {
      const pages = g.pages.map((p) => p.page).filter((p): p is number => p != null)
      const pageStr = pages.length ? ` (p${pages.join(', p')})` : ''
      lines.push(`[${g.n}] ${g.title}${pageStr}`)
    }
  }
  return lines.join('\n')
}

function AnswerSkeleton() {
  return (
    <div className="mt-6 space-y-4" data-testid="ask-loading">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Spinner className="h-4 w-4" /> Searching your documents…
      </div>
      <div className="space-y-2 rounded-lg border border-border bg-card p-4">
        <div className="h-3.5 w-3/4 animate-pulse rounded bg-muted" />
        <div className="h-3.5 w-full animate-pulse rounded bg-muted" />
        <div className="h-3.5 w-5/6 animate-pulse rounded bg-muted" />
        <div className="h-3.5 w-2/3 animate-pulse rounded bg-muted" />
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
      aria-pressed={props.active}
      title={props.label}
      className={`flex h-7 items-center gap-1 rounded px-2 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
        props.active ? 'bg-primary/10 text-primary' : 'hover:bg-muted'
      } disabled:opacity-50`}
    >
      {props.icon}
    </button>
  )
}

// A clickable, hover-previewable citation chip rendered inline in the
// answer text. Deep-links to the cited page in the document viewer.
function CitationBadge({ group, page, snippet }: { group: CitationGroup; page?: number; snippet: string }) {
  const label = page != null ? `${group.n}·p${page}` : `${group.n}`
  const inner = (
    <span
      className="ms-0.5 inline-flex items-center rounded-full bg-primary/10 px-1.5 align-super text-[10px] font-semibold text-primary ring-1 ring-inset ring-primary/20 transition-colors hover:bg-primary/20"
    >
      {label}
    </span>
  )
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        {group.workspace_id ? (
          <Link
            to="/workspaces/$workspaceId/documents/$documentId"
            params={{ workspaceId: group.workspace_id, documentId: group.doc_id }}
            search={page != null ? { page } : {}}
            className="rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            data-testid="ask-inline-citation"
            aria-label={`Source ${group.n}${page != null ? `, page ${page}` : ''}: ${group.title}`}
          >
            {inner}
          </Link>
        ) : (
          <span aria-label={`Source ${group.n}: ${group.title}`}>{inner}</span>
        )}
      </TooltipTrigger>
      <TooltipContent className="max-w-xs">
        <p className="font-medium">{group.title}{page != null ? ` · page ${page}` : ''}</p>
        {cleanSnippet(snippet) && <p className="mt-1 line-clamp-4 text-muted-foreground">{cleanSnippet(snippet)}</p>}
      </TooltipContent>
    </Tooltip>
  )
}

function renderAnswerWithInlineCitations(text: string, byDoc: Map<string, CitationGroup>) {
  const parts: React.ReactNode[] = []
  const re = /\[([0-9a-f-]{8,}(?::page_\d+)?)\]/gi
  // Tighten a space the model leaves before trailing punctuation after a
  // citation ("[1·p1] ." → "[1·p1].").
  const tightenAfterChip = (s: string) => s.replace(/^[ \t]+([.,;:!?)\]…])/, '$1')
  let lastIdx = 0
  let m: RegExpExecArray | null
  let key = 0
  let prevChipKey: string | null = null // last rendered chip (doc:page) for adjacent-dedup
  let prevWasChip = false
  while ((m = re.exec(text)) !== null) {
    let gap = text.slice(lastIdx, m.index)
    const token = m[1]
    const [docId, pagePart] = token.split(':page_')
    const group = byDoc.get(docId)
    const page = pagePart ? Number(pagePart) : undefined
    const chipKey = group ? `${docId}:${page ?? ''}` : null

    // Merge adjacent duplicate chips: same doc+page with only whitespace
    // between them collapses to a single chip.
    if (group && chipKey === prevChipKey && /^\s*$/.test(gap)) {
      lastIdx = m.index + m[0].length
      continue
    }

    if (gap) {
      if (prevWasChip) gap = tightenAfterChip(gap)
      parts.push(gap)
      if (gap.trim()) { prevChipKey = null; prevWasChip = false }
    }

    if (group) {
      const match = page != null ? group.pages.find((p) => p.page === page) : undefined
      const snippet = match?.snippet ?? group.pages[0]?.snippet ?? ''
      parts.push(<CitationBadge key={`cite-${key++}`} group={group} page={page} snippet={snippet} />)
      prevChipKey = chipKey
      prevWasChip = true
    } else {
      // Unmatched token (LLM referenced a doc not in citations) — keep
      // the raw text rather than rendering a broken link.
      parts.push(m[0])
      prevChipKey = null
      prevWasChip = false
    }
    lastIdx = m.index + m[0].length
  }
  if (lastIdx < text.length) {
    parts.push(prevWasChip ? tightenAfterChip(text.slice(lastIdx)) : text.slice(lastIdx))
  }
  return parts
}

function CitationsList({ groups, heading }: { groups: CitationGroup[]; heading: string }) {
  if (!groups.length) return null
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <h3 className="mb-3 text-sm font-medium">{heading}</h3>
      <ul className="space-y-3" data-testid="ask-citations-list">
        {groups.map((g) => (
          <CitationRow key={g.doc_id} group={g} />
        ))}
      </ul>
    </div>
  )
}

function CitationRow({ group }: { group: CitationGroup }) {
  const [expanded, setExpanded] = useState(false)
  const pagesWithNum = group.pages.filter((p) => p.page != null)
  const primarySnippet = cleanSnippet(group.pages[0]?.snippet)
  const hasMore = group.pages.length > 1

  return (
    <li className="flex flex-col gap-1 text-sm">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="font-mono text-xs text-muted-foreground">[{group.n}]</span>
        {group.workspace_id ? (
          <Link
            to="/workspaces/$workspaceId/documents/$documentId"
            params={{ workspaceId: group.workspace_id, documentId: group.doc_id }}
            search={pagesWithNum[0]?.page != null ? { page: pagesWithNum[0].page } : {}}
            className="font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {group.title}
          </Link>
        ) : (
          <span className="font-medium">{group.title}</span>
        )}

        {/* Per-page deep links so each cited page is reachable directly. */}
        {pagesWithNum.length > 0 && group.workspace_id && (
          <span className="flex flex-wrap items-center gap-1">
            {pagesWithNum.map((p) => (
              <Link
                key={p.page}
                to="/workspaces/$workspaceId/documents/$documentId"
                params={{ workspaceId: group.workspace_id!, documentId: group.doc_id }}
                search={{ page: p.page! }}
                className="rounded border border-border px-1.5 text-xs text-muted-foreground transition-colors hover:border-primary hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                title={`Open page ${p.page}`}
              >
                p{p.page}
              </Link>
            ))}
          </span>
        )}

        <Tooltip>
          <TooltipTrigger asChild>
            <span className="ms-auto rounded bg-muted px-1.5 text-xs tabular-nums text-muted-foreground" data-testid="ask-source-score">
              {formatRelevance(group.bestScore)}
            </span>
          </TooltipTrigger>
          <TooltipContent>Relevance score</TooltipContent>
        </Tooltip>
      </div>

      <p className="text-xs text-muted-foreground">{primarySnippet}</p>

      {expanded && hasMore && (
        <ul className="mt-1 space-y-1 border-s border-border ps-3">
          {group.pages.slice(1).map((p, i) => (
            <li key={`${p.page}-${i}`} className="text-xs text-muted-foreground">
              {p.page != null && <span className="me-1 font-medium text-foreground">p{p.page}:</span>}
              {cleanSnippet(p.snippet)}
            </li>
          ))}
        </ul>
      )}

      {hasMore && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="self-start rounded text-xs font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {expanded ? 'Show less' : `Show ${group.pages.length - 1} more passage${group.pages.length - 1 === 1 ? '' : 's'}`}
        </button>
      )}
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/ask')({ component: AskPage })
