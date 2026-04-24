// Prompt 16 / §17 — side-by-side version comparison.
//
// Text source: pdfjs.getDocument() on each version's download URL; we
// pull text from every page and concatenate. No new backend endpoint.
// The download URL is same-origin so the session cookie flows through
// automatically.
//
// Diff: diff-match-patch. diff_cleanupSemantic() merges character-level
// runs into readable word-level chunks before render.
//
// Constraint: read-only comparison. No editing, no annotation authoring
// inside this view (that's the PDFViewer's job).

import { useEffect, useMemo, useState } from 'react'
// @types/diff-match-patch uses `export = diff_match_patch`; without
// `esModuleInterop` a default import fails tsc. Named import is safe.
import { diff_match_patch } from 'diff-match-patch'
import { pdfjs } from 'react-pdf'
import { X } from 'lucide-react'

import { Button } from '@/components/ui/Button'
import { Spinner } from '@/components/ui/Spinner'

interface VersionCompareProps {
  documentId: string
  leftVersionId: string
  leftLabel: string
  rightVersionId: string
  rightLabel: string
  onClose: () => void
}

type DiffOp = 'ins' | 'del' | 'eq'
interface DiffRun { op: DiffOp; text: string }

async function extractText(url: string): Promise<string> {
  const loadingTask = pdfjs.getDocument(url)
  const doc = await loadingTask.promise
  const pages: string[] = []
  for (let i = 1; i <= doc.numPages; i++) {
    const page = await doc.getPage(i)
    const content = await page.getTextContent()
    // `items` is Array<TextItem | TextMarkedContent>; narrow to TextItem.
    const pageText = content.items
      .map((it) => ('str' in it ? it.str : ''))
      .join(' ')
    pages.push(pageText)
  }
  await doc.destroy()
  return pages.join('\n\n')
}

function runDiff(a: string, b: string): DiffRun[] {
  const dmp = new diff_match_patch()
  const raw = dmp.diff_main(a, b)
  dmp.diff_cleanupSemantic(raw)
  return raw.map(([op, text]) => ({
    op: op === 1 ? 'ins' : op === -1 ? 'del' : 'eq',
    text,
  }))
}

export function VersionCompare({
  documentId, leftVersionId, leftLabel, rightVersionId, rightLabel, onClose,
}: VersionCompareProps) {
  const [leftText, setLeftText] = useState<string | null>(null)
  const [rightText, setRightText] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const leftURL = `/api/v1/documents/${documentId}/versions/${leftVersionId}/download`
    const rightURL = `/api/v1/documents/${documentId}/versions/${rightVersionId}/download`
    Promise.all([extractText(leftURL), extractText(rightURL)])
      .then(([l, r]) => { if (!cancelled) { setLeftText(l); setRightText(r) } })
      .catch((err) => { if (!cancelled) setError(err?.message ?? 'Failed to extract text') })
    return () => { cancelled = true }
  }, [documentId, leftVersionId, rightVersionId])

  const diffs = useMemo(() => {
    if (leftText == null || rightText == null) return null
    return runDiff(leftText, rightText)
  }, [leftText, rightText])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-[var(--color-bg)]">
      <header className="flex items-center justify-between border-b border-[var(--color-border)] px-4 py-3">
        <div>
          <h2 className="text-base font-semibold">Compare versions</h2>
          <p className="text-xs text-[var(--color-text-secondary)]">
            {leftLabel} <span className="mx-1">→</span> {rightLabel}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close compare view">
          <X className="h-4 w-4" />
        </Button>
      </header>

      {error ? (
        <div className="flex flex-1 items-center justify-center p-6 text-sm text-red-500">{error}</div>
      ) : diffs == null ? (
        <div className="flex flex-1 items-center justify-center gap-2 p-6 text-sm text-[var(--color-text-secondary)]">
          <Spinner /> Extracting text…
        </div>
      ) : (
        <div className="grid flex-1 grid-cols-2 gap-px overflow-auto bg-[var(--color-border)]">
          <PaneView side="left" runs={diffs} label={leftLabel} />
          <PaneView side="right" runs={diffs} label={rightLabel} />
        </div>
      )}
    </div>
  )
}

// PaneView renders the left or right side of the diff. The left pane
// shows equal + deleted runs; the right pane shows equal + inserted
// runs. Same scroll container by column, so eyeballing aligns the
// unchanged text naturally.
function PaneView({ side, runs, label }: { side: 'left' | 'right'; runs: DiffRun[]; label: string }) {
  const visible = runs.filter((r) => r.op === 'eq' || r.op === (side === 'left' ? 'del' : 'ins'))
  return (
    <section className="overflow-auto bg-[var(--color-bg-secondary)] p-4">
      <h3 className="mb-2 text-xs font-medium uppercase text-[var(--color-text-secondary)]">{label}</h3>
      <div className="whitespace-pre-wrap break-words font-mono text-sm leading-relaxed">
        {visible.map((run, i) => (
          <span
            key={i}
            className={
              run.op === 'eq'
                ? ''
                : run.op === 'ins'
                ? 'bg-green-200 text-green-900 dark:bg-green-900/40 dark:text-green-100'
                : 'bg-red-200 text-red-900 line-through dark:bg-red-900/40 dark:text-red-100'
            }
          >
            {run.text}
          </span>
        ))}
      </div>
    </section>
  )
}
