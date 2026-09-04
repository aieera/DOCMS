// MatchedClausesPanel — ADR 0104 Phase 2 surface. Shows clause-library
// matches detected in this document (written by the intelligence
// detect_clauses task). Self-hides when there are no matches, matching
// the FilingSuggestionPanel idiom. Copy puts the MATCHED text on the
// clipboard (the Phase-3 "reuse" substitute).
import { copyText } from '@/lib/clipboard'
import { useQuery } from '@tanstack/react-query'
import { BookMarked, Copy, ShieldCheck } from 'lucide-react'
import { toast } from 'sonner'

import { getDocumentClauseMatches } from '@/api/clauses'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'

export function MatchedClausesPanel({ documentId }: { documentId: string }) {
  const { data: matches } = useQuery({
    queryKey: ['clause-matches', documentId],
    queryFn: () => getDocumentClauseMatches(documentId),
    staleTime: 60_000,
  })

  if (!matches?.length) return null

  const copy = async (text: string) => {
    try {
      await copyText(text)
      toast.success('Copied to clipboard')
    } catch {
      toast.error('Copy failed')
    }
  }

  return (
    <Card className="p-4" data-testid="matched-clauses-panel">
      <div className="mb-2 flex items-center gap-2 text-sm font-semibold">
        <BookMarked className="h-4 w-4" /> Matched clauses
      </div>
      <ul className="space-y-3">
        {matches.map((m) => (
          <li key={`${m.clause_id}-${m.chunk_index}`} className="rounded-lg border border-border/60 p-2.5">
            <div className="flex items-center justify-between gap-2">
              <span className="truncate text-sm font-medium">{m.clause_name}</span>
              <span className="shrink-0 text-xs text-muted-foreground">
                {Math.round(m.similarity * 100)}%
              </span>
            </div>
            <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
              {m.approved && (
                <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-1.5 py-0.5 text-success">
                  <ShieldCheck className="h-3 w-3" /> Approved
                </span>
              )}
              {m.jurisdiction && (
                <span className="text-muted-foreground">{m.jurisdiction}</span>
              )}
            </div>
            {m.matched_text && (
              <p className="mt-1.5 line-clamp-3 text-xs text-muted-foreground">{m.matched_text}</p>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="mt-1.5 h-7 gap-1.5 px-2 text-xs"
              onClick={() => copy(m.matched_text)}
            >
              <Copy className="h-3 w-3" /> Copy
            </Button>
          </li>
        ))}
      </ul>
    </Card>
  )
}
