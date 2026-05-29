import type { Citation } from '@/api/doc-qa'

interface Props {
  citations: Citation[]
  /** Optional handler — when provided, click on a citation calls this
   * with the citation so the document viewer can scroll/highlight.
   * The viewer integration is a follow-up (pdf.js text-layer span). */
  onJumpTo?: (c: Citation) => void
}

export function CitationList({ citations, onJumpTo }: Props) {
  if (citations.length === 0) return null
  return (
    <ol className="mt-3 space-y-1 rounded-xl border border-border/60 bg-background/60 p-3 text-xs">
      <div className="mb-1 text-muted-foreground">Citations</div>
      {citations.map((c, i) => (
        <li key={`${c.document_id}-${c.chunk_index}-${i}`}>
          <button
            onClick={() => onJumpTo?.(c)}
            disabled={!onJumpTo}
            className={[
              'text-start',
              onJumpTo ? 'text-primary hover:underline' : 'cursor-default text-foreground',
            ].join(' ')}
            title={c.text}
          >
            <span className="font-mono text-[10px] text-muted-foreground">
              [{i + 1}]
              {c.page ? ` p.${c.page}` : ''}
            </span>{' '}
            {truncate(c.text, 140)}
          </button>
        </li>
      ))}
    </ol>
  )
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}
