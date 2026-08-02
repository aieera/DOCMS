import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Sparkles, X } from 'lucide-react'
import { toast } from 'sonner'

import { listTagSuggestions, reviewTagSuggestions } from '@/api/intelligence'
import { Button } from '@/components/ui/shadcn/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/shadcn/popover'

// "✨ N suggested" pill for document cards: pending AI tag suggestions
// with one-click accept/reject, without opening the document. Renders
// nothing when the document has no pending suggestions so grids stay
// clean. Lives inside the card's <Link>, so every interactive element
// stops propagation and prevents default — a click here must never
// navigate into the viewer.
export function TagSuggestionBadge({ documentId }: { documentId: string }) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['tag-suggestions', documentId],
    queryFn: () => listTagSuggestions(documentId),
    staleTime: 60_000,
    retry: false,
  })

  const review = useMutation({
    mutationFn: (actions: { suggestion_id: string; action: 'accept' | 'reject' }[]) =>
      reviewTagSuggestions(documentId, actions),
    onSuccess: (res) => {
      if (res.accepted_tags.length > 0) toast.success(`Tagged: ${res.accepted_tags.join(', ')}`)
      qc.invalidateQueries({ queryKey: ['tag-suggestions', documentId] })
      qc.invalidateQueries({ queryKey: ['documents'] })
    },
    onError: () => toast.error('Could not save the tag decision — try again'),
  })

  const pending = (q.data?.suggestions ?? []).filter((s) => s.status === 'pending')
  if (pending.length === 0) return null

  const swallow = (e: React.SyntheticEvent) => {
    e.preventDefault()
    e.stopPropagation()
  }

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          // stopPropagation only: bubbling to the card's <Link> is what
          // navigates; preventDefault would ALSO cancel Radix's own
          // composed click handler (it skips when defaultPrevented) and
          // the popover would never open.
          onClick={(e) => e.stopPropagation()}
          className="inline-flex items-center gap-1 rounded-full border border-primary/40 bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary transition-colors hover:bg-primary/20"
          aria-label={`${pending.length} AI tag suggestions — review`}
          data-testid={`tag-suggestion-badge-${documentId}`}
        >
          <Sparkles className="h-3 w-3" aria-hidden />
          {pending.length} suggested
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-72 p-2" onClick={swallow}>
        <p className="px-2 pb-2 pt-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Suggested tags
        </p>
        <ul className="space-y-1">
          {pending.map((s) => (
            <li key={s.id} className="flex items-center justify-between gap-2 rounded-md px-2 py-1 hover:bg-muted/60">
              {/* The name is the identity of the row: give it the full
                  remaining width and drop the confidence to a second
                  line, so "contract" no longer truncates to "contr…"
                  next to the chip and two icon buttons. */}
              <span className="flex min-w-0 flex-1 flex-col" title={s.tag_name}>
                <span className="truncate text-sm">{s.tag_name}</span>
                <span className="text-xs text-muted-foreground">
                  {Math.round(s.confidence * 100)}% confidence
                </span>
              </span>
              <span className="flex shrink-0 items-center gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0 text-emerald-600 hover:text-emerald-700"
                  aria-label={`Accept tag ${s.tag_name}`}
                  disabled={review.isPending}
                  onClick={(e) => {
                    swallow(e)
                    review.mutate([{ suggestion_id: s.id, action: 'accept' }])
                  }}
                >
                  <Check className="h-4 w-4" aria-hidden />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-7 w-7 p-0 text-muted-foreground hover:text-destructive"
                  aria-label={`Reject tag ${s.tag_name}`}
                  disabled={review.isPending}
                  onClick={(e) => {
                    swallow(e)
                    review.mutate([{ suggestion_id: s.id, action: 'reject' }])
                  }}
                >
                  <X className="h-4 w-4" aria-hidden />
                </Button>
              </span>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
