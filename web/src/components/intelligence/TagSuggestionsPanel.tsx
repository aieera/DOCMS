import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Check, Sparkles, X } from 'lucide-react'

import {
  listTagSuggestions,
  reviewTagSuggestions,
  type ReviewAction,
  type TagSuggestion,
} from '@/api/intelligence'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'

interface Props {
  documentId: string
}

const HIGH_CONFIDENCE = 0.9
const MEDIUM_CONFIDENCE = 0.7

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

  const { data, isLoading } = useQuery({
    queryKey: ['tag-suggestions', documentId],
    queryFn: () => listTagSuggestions(documentId),
    refetchInterval: 10_000,
  })

  const review = useMutation({
    mutationFn: (actions: ReviewAction[]) => reviewTagSuggestions(documentId, actions),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['tag-suggestions', documentId] })
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
  const autoApplied = useMemo(
    () => (data?.suggestions ?? []).filter((s) => s.status === 'auto_applied'),
    [data],
  )

  if (isLoading) {
    return (
      <div className="rounded border border-zinc-200 p-4 text-sm text-zinc-500">
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
          <span className="ml-1 text-xs text-zinc-500">({pending.length} pending)</span>
        )}
      </div>

      {autoApplied.length > 0 && (
        <div className="border-b border-zinc-100 px-4 py-3 dark:border-zinc-900">
          <div className="mb-2 text-xs uppercase tracking-wide text-zinc-500">Auto-applied</div>
          <div className="flex flex-wrap gap-2">
            {autoApplied.map((s) => (
              <Badge key={s.id} variant="default" className="gap-1">
                <Check className="h-3 w-3" />
                {s.tag_name}
                <span className="ml-1 text-[10px] opacity-70">
                  {Math.round(s.confidence * 100)}% · {sourceLabel(s.source)}
                </span>
              </Badge>
            ))}
          </div>
        </div>
      )}

      {pending.length > 0 && (
        <>
          <ul className="divide-y divide-zinc-100 dark:divide-zinc-900">
            {pending.map((s) => {
              const busy = busyIds.has(s.id)
              const pct = Math.round(s.confidence * 100)
              return (
                <li key={s.id} className="flex items-center gap-3 px-4 py-2">
                  <div
                    aria-hidden
                    className={`h-2 w-2 rounded-full ${bandColor(s.confidence)}`}
                    title={`Confidence ${pct}%`}
                  />
                  <span className="flex-1 truncate text-sm" title={s.tag_name}>
                    {s.tag_name}
                  </span>
                  <span className="w-12 text-right text-xs tabular-nums text-zinc-500">{pct}%</span>
                  <Badge variant="outline" className="text-[10px]">
                    {sourceLabel(s.source)}
                  </Badge>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => submit([{ suggestion_id: s.id, action: 'accept' }])}
                    aria-label={`Accept ${s.tag_name}`}
                  >
                    <Check className="h-4 w-4 text-emerald-600" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => submit([{ suggestion_id: s.id, action: 'reject' }])}
                    aria-label={`Reject ${s.tag_name}`}
                  >
                    <X className="h-4 w-4 text-zinc-500" />
                  </Button>
                </li>
              )
            })}
          </ul>
          <div className="flex items-center gap-2 border-t border-zinc-100 px-4 py-2 dark:border-zinc-900">
            <Button
              size="sm"
              variant="outline"
              disabled={review.isPending || pending.every((s) => s.confidence < HIGH_CONFIDENCE)}
              onClick={acceptHighConfidence}
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
        </>
      )}
    </div>
  )
}
