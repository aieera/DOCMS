import { useMemo, useState } from 'react'
import { invalidateTagSuggestions } from '@/hooks/queryInvalidation'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { Check, ChevronRight, Sparkles, X } from 'lucide-react'

import {
  listTagSuggestions,
  reviewTagSuggestions,
  type ReviewAction,
  type TagSuggestion,
} from '@/api/intelligence'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
}

const HIGH_CONFIDENCE = 0.9
const MEDIUM_CONFIDENCE = 0.7
// Above this many pending suggestions the flat list becomes a wall (NER
// can emit 20+ per document) — switch to per-category collapsed groups.
const GROUP_THRESHOLD = 5

// Tags follow a `category:value` convention (party_name:acme). The
// prefix is the natural grouping key; uncategorised tags pool under
// "other".
function categoryOf(tag: string): string {
  const i = tag.indexOf(':')
  return i > 0 ? tag.slice(0, i) : 'other'
}

function bandColor(confidence: number): string {
  if (confidence >= HIGH_CONFIDENCE) return 'bg-emerald-500'
  if (confidence >= MEDIUM_CONFIDENCE) return 'bg-amber-500'
  return 'bg-orange-500'
}

function sourceLabel(source: TagSuggestion['source']): string {
  return ({ ner: 'NER', classification: 'Class', llm: 'LLM', pattern: 'Pat' } as const)[source]
}

export function TagSuggestionsPanel({ documentId }: Props) {
  const qc = useQueryClient()
  const [busyIds, setBusyIds] = useState<Set<string>>(new Set())
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())

  const { data, isLoading } = useQuery({
    queryKey: ['tag-suggestions', documentId],
    queryFn: () => listTagSuggestions(documentId),
    // Poll only while suggestions are still being generated — once the
    // pipeline has produced any rows, stop polling. The mutation that
    // accepts/rejects already invalidates on success, so the panel
    // refreshes on user action without needing the background tick.
    refetchInterval: (q) => {
      const arr = q.state.data as unknown[] | undefined
      return arr === undefined || arr.length === 0 ? 15_000 : false
    },
    staleTime: 60_000,
    refetchOnWindowFocus: false,
  })

  const review = useAppMutation({
    mutationFn: (actions: ReviewAction[]) => reviewTagSuggestions(documentId, actions),
    onSuccess: (res) => {
      void invalidateTagSuggestions(qc)
      qc.invalidateQueries({ queryKey: ['document', documentId] })
      const acc = res.accepted_count
      const rej = res.rejected_count
      toast.success(
        `${acc ? `Accepted ${acc} ` : ''}${acc && rej ? '· ' : ''}${rej ? `Rejected ${rej}` : ''}`.trim() ||
          'Reviewed',
      )
    },
    onError: () => toast.error('Review failed'),
    onSettled: () => setBusyIds(new Set()),
  })

  const pending = useMemo(
    () => (data?.suggestions ?? []).filter((s) => s.status === 'pending'),
    [data],
  )
  // Category groups, biggest first — stable presentation for long lists.
  const groups = useMemo(() => {
    const m = new Map<string, TagSuggestion[]>()
    for (const s of pending) {
      const cat = categoryOf(s.tag_name)
      const arr = m.get(cat)
      if (arr) arr.push(s)
      else m.set(cat, [s])
    }
    return [...m.entries()].sort((a, b) => b[1].length - a[1].length || a[0].localeCompare(b[0]))
  }, [pending])
  const grouped = pending.length > GROUP_THRESHOLD
  const autoApplied = useMemo(
    () => (data?.suggestions ?? []).filter((s) => s.status === 'auto_applied'),
    [data],
  )

  if (isLoading) {
    return (
      <div className="rounded border border-zinc-200 p-4 text-sm text-zinc-600">
        Loading tag suggestions…
      </div>
    )
  }

  if (pending.length === 0 && autoApplied.length === 0) {
    return null
  }

  const submit = (actions: ReviewAction[]) => {
    if (actions.length === 0) return
    setBusyIds(new Set(actions.map((a) => a.suggestion_id)))
    review.mutate(actions)
  }

  const acceptHighConfidence = () => {
    const high = pending.filter((s) => s.confidence >= HIGH_CONFIDENCE)
    submit(high.map((s) => ({ suggestion_id: s.id, action: 'accept' })))
  }

  const dismissAll = () => {
    submit(pending.map((s) => ({ suggestion_id: s.id, action: 'reject' })))
  }

  return (
    <div className="rounded border border-zinc-200 dark:border-zinc-800">
      <div className="flex items-center gap-2 border-b border-zinc-200 px-4 py-2 text-sm font-medium dark:border-zinc-800">
        <Sparkles className="h-4 w-4 text-violet-500" />
        Suggested tags
        {pending.length > 0 && (
          <span className="ms-1 text-xs text-zinc-600">({pending.length} pending)</span>
        )}
      </div>

      {autoApplied.length > 0 && (
        <div className="border-b border-zinc-100 px-4 py-3 dark:border-zinc-900">
          <div className="mb-2 text-xs uppercase tracking-wide text-zinc-600">Auto-applied</div>
          <div className="flex flex-wrap gap-2">
            {autoApplied.map((s) => (
              <Badge key={s.id} variant="default" className="gap-1">
                <Check className="h-3 w-3" />
                {s.tag_name}
                <span className="ms-1 text-[10px] opacity-70">
                  {Math.round(s.confidence * 100)}% · {sourceLabel(s.source)}
                </span>
              </Badge>
            ))}
          </div>
        </div>
      )}

      {pending.length > 0 && (
        <>
          {/* Bulk actions live at the TOP: with 20+ rows the old
              bottom placement scrolled out of sight, which is exactly
              when you need them. */}
          <div className="flex items-center gap-2 border-b border-zinc-100 px-4 py-2 dark:border-zinc-900">
            {/* A disabled control with no reason reads as broken. Say
                why: usually "nothing here clears the 90% bar". */}
            <Button
              size="sm"
              variant="outline"
              disabled={review.isPending || pending.every((s) => s.confidence < HIGH_CONFIDENCE)}
              onClick={acceptHighConfidence}
              title={
                pending.every((s) => s.confidence < HIGH_CONFIDENCE)
                  ? `No suggestion is at or above ${Math.round(HIGH_CONFIDENCE * 100)}% confidence yet — review them individually.`
                  : `Accept every suggestion at or above ${Math.round(HIGH_CONFIDENCE * 100)}% confidence`
              }
            >
              Accept all high-confidence
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={review.isPending}
              onClick={dismissAll}
            >
              Dismiss all
            </Button>
          </div>

          {grouped ? (
            <div className="divide-y divide-zinc-100 dark:divide-zinc-900">
              {groups.map(([cat, items]) => {
                const open = openGroups.has(cat)
                return (
                  <div key={cat}>
                    <div className="flex items-center gap-1 px-2 py-1">
                      <button
                        type="button"
                        onClick={() =>
                          setOpenGroups((prev) => {
                            const next = new Set(prev)
                            if (next.has(cat)) next.delete(cat)
                            else next.add(cat)
                            return next
                          })
                        }
                        aria-expanded={open}
                        aria-label={`${cat} (${items.length})`}
                        className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md px-2 py-1.5 text-start text-sm hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        <ChevronRight
                          className={`h-3.5 w-3.5 shrink-0 text-zinc-400 transition-transform ${open ? 'rotate-90' : ''}`}
                        />
                        <span className="truncate font-medium">{cat}</span>
                        <span className="text-xs text-zinc-600">({items.length})</span>
                      </button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={review.isPending}
                        onClick={() => submit(items.map((s) => ({ suggestion_id: s.id, action: 'accept' as const })))}
                        aria-label={`Accept all ${cat} suggestions`}
                        title={`Accept all ${cat}`}
                      >
                        <Check className="h-4 w-4 text-emerald-600" />
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={review.isPending}
                        onClick={() => submit(items.map((s) => ({ suggestion_id: s.id, action: 'reject' as const })))}
                        aria-label={`Dismiss all ${cat} suggestions`}
                        title={`Dismiss all ${cat}`}
                      >
                        <X className="h-4 w-4 text-zinc-600" />
                      </Button>
                    </div>
                    {open && (
                      <ul className="divide-y divide-zinc-100 border-t border-zinc-100 dark:divide-zinc-900 dark:border-zinc-900">
                        {items.map((s) => (
                          <SuggestionRow
                            key={s.id}
                            s={s}
                            busy={busyIds.has(s.id)}
                            onAccept={() => submit([{ suggestion_id: s.id, action: 'accept' }])}
                            onReject={() => submit([{ suggestion_id: s.id, action: 'reject' }])}
                          />
                        ))}
                      </ul>
                    )}
                  </div>
                )
              })}
            </div>
          ) : (
            <ul className="divide-y divide-zinc-100 dark:divide-zinc-900">
              {pending.map((s) => (
                <SuggestionRow
                  key={s.id}
                  s={s}
                  busy={busyIds.has(s.id)}
                  onAccept={() => submit([{ suggestion_id: s.id, action: 'accept' }])}
                  onReject={() => submit([{ suggestion_id: s.id, action: 'reject' }])}
                />
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}

function SuggestionRow({
  s,
  busy,
  onAccept,
  onReject,
}: {
  s: TagSuggestion
  busy: boolean
  onAccept: () => void
  onReject: () => void
}) {
  const pct = Math.round(s.confidence * 100)
  return (
    <li className="flex items-center gap-3 px-4 py-2">
      <div
        aria-hidden
        className={`h-2 w-2 shrink-0 rounded-full ${bandColor(s.confidence)}`}
        title={`Confidence ${pct}%`}
      />
      <span className="min-w-0 flex-1 truncate text-sm" title={s.tag_name}>
        {s.tag_name}
      </span>
      <span className="w-10 shrink-0 text-end text-xs tabular-nums text-zinc-600">{pct}%</span>
      <Badge variant="outline" className="shrink-0 text-[10px]">
        {sourceLabel(s.source)}
      </Badge>
      <Button size="sm" variant="ghost" disabled={busy} onClick={onAccept} aria-label={`Accept ${s.tag_name}`}>
        <Check className="h-4 w-4 text-emerald-600" />
      </Button>
      <Button size="sm" variant="ghost" disabled={busy} onClick={onReject} aria-label={`Reject ${s.tag_name}`}>
        <X className="h-4 w-4 text-zinc-600" />
      </Button>
    </li>
  )
}
