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
    <ol className="mt-3 space-y-1 rounded bg-zinc-50 p-3 text-xs dark:bg-zinc-900">
      <div className="mb-1 text-zinc-500">Citations</div>
      {citations.map((c, i) => (
        <li key={`${c.document_id}-${c.chunk_index}-${i}`}>
          <button
            onClick={() => onJumpTo?.(c)}
            disabled={!onJumpTo}
            className={[
              'text-start',
              onJumpTo ? 'text-violet-600 hover:underline' : 'cursor-default',
            ].join(' ')}
            title={c.text}
          >
            <span className="font-mono text-[10px] text-zinc-500">
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
