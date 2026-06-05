// Cross-format compare dialog — ADR 0101 §18 F8.
//
// User flow:
//   1. Open from doc detail header → modal shows current doc on the left.
//   2. Search-as-you-type to pick a second doc (right side).
//   3. Click Compare → POST /compare → render diff in two side-by-side
//      panes (left = original-side equals+deletes, right = other-side
//      equals+inserts).
//
// Phase 1 ships paragraph-level diff only. The granularity toggle is
// present but the "word" and "semantic" options are disabled with a
// tooltip — they require a different renderer (Phase 2 in ADR 0101).
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { ArrowLeftRight, Loader2, Search as SearchIcon } from 'lucide-react'

import { compareDocuments, type CompareResponse, type DiffOperation } from '@/api/compare'
import { api } from '@/api/client'
import { Dialog } from '@/components/ui/Dialog'
import { Button } from '@/components/ui/shadcn/button'
import { Input } from '@/components/ui/shadcn/input'

interface SearchHit {
  document_id: string
  title?: string
  workspace_name?: string
  mime_type?: string
}

interface Props {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** The "left side" — comes from the doc detail page. */
  baseDocumentId: string
  baseDocumentTitle: string
}

export function CompareDialog({ open, onOpenChange, baseDocumentId, baseDocumentTitle }: Props) {
  const [otherId, setOtherId] = useState<string | null>(null)
  const [otherTitle, setOtherTitle] = useState<string>('')
  const [pickerQ, setPickerQ] = useState('')

  const search = useQuery({
    queryKey: ['compare-picker', pickerQ],
    queryFn: async () => {
      if (pickerQ.trim().length < 2) return [] as SearchHit[]
      // /search returns {results: SearchHit[], facets, total_count, ...}
      // (per services/search response shape). Earlier this hook looked
      // for `data.hits` which is the OpenSearch raw-doc shape, not
      // ours — fix is to read `data.results`.
      const { data } = await api.get<{
        results?: SearchHit[]
        hits?: SearchHit[]
      } | SearchHit[]>(
        `/search?q=${encodeURIComponent(pickerQ)}&limit=10`,
      )
      const list = Array.isArray(data)
        ? data
        : data?.results ?? data?.hits ?? []
      return list.filter((h) => h.document_id && h.document_id !== baseDocumentId)
    },
    enabled: open && pickerQ.trim().length >= 2,
    staleTime: 5_000,
  })

  const compare = useAppMutation({
    mutationFn: () => {
      if (!otherId) throw new Error('pick a second document')
      return compareDocuments({
        doc_a_id: baseDocumentId,
        doc_b_id: otherId,
        granularity: 'paragraph',
      })
    },
  })

  const reset = () => {
    setOtherId(null)
    setOtherTitle('')
    setPickerQ('')
    compare.reset()
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => { if (!o) reset(); onOpenChange(o) }}
      title={`Compare "${baseDocumentTitle}" with…`}
    >
      <div className="flex max-h-[80vh] flex-col gap-4 overflow-hidden">
        {!compare.data && (
          <>
            <div className="flex items-center gap-2">
              <SearchIcon className="h-4 w-4 text-muted-foreground" />
              <Input
                value={pickerQ}
                onChange={(e) => setPickerQ(e.target.value)}
                placeholder="Search for the other document…"
                autoFocus
              />
            </div>
            <ul className="max-h-56 overflow-y-auto rounded-md border border-border bg-card text-sm">
              {search.isLoading && pickerQ.length >= 2 && (
                <li className="p-3 text-muted-foreground">Searching…</li>
              )}
              {search.data?.length === 0 && pickerQ.length >= 2 && !search.isLoading && (
                <li className="p-3 text-muted-foreground">No matches.</li>
              )}
              {search.data?.map((h) => (
                <li key={h.document_id}>
                  <button
                    type="button"
                    onClick={() => { setOtherId(h.document_id); setOtherTitle(h.title || h.document_id) }}
                    className={`flex w-full flex-col gap-0.5 p-2 text-start hover:bg-accent ${
                      otherId === h.document_id ? 'bg-accent' : ''
                    }`}
                  >
                    <span className="truncate">{h.title || '(untitled)'}</span>
                    <span className="text-xs text-muted-foreground">
                      {h.workspace_name ?? ''} · {h.mime_type ?? ''}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
            <Button
              onClick={() => compare.mutate()}
              disabled={!otherId || compare.isPending}
              className="self-end"
            >
              {compare.isPending ? <Loader2 className="me-2 h-4 w-4 animate-spin" /> : <ArrowLeftRight className="me-2 h-4 w-4" />}
              {compare.isPending ? 'Comparing…' : `Compare with ${otherTitle || 'document'}`}
            </Button>
            {compare.isError && (
              <p className="text-sm text-destructive">{(compare.error as Error).message}</p>
            )}
          </>
        )}

        {compare.data && <DiffPanes data={compare.data} onBack={reset} />}
      </div>
    </Dialog>
  )
}

function DiffPanes({ data, onBack }: { data: CompareResponse; onBack: () => void }) {
  // Render two views of the same operation list. Left pane shows
  // equal + delete (the original); right pane shows equal + insert
  // (the other doc). This is the simplest possible side-by-side; a
  // synchronized-scroll layout with line gutters is a Phase 2 polish.
  const left = useMemo(() => filterOps(data.diff.operations, 'delete'), [data])
  const right = useMemo(() => filterOps(data.diff.operations, 'insert'), [data])

  return (
    <div className="flex flex-col gap-3 overflow-hidden">
      <header className="flex items-center justify-between border-b border-border pb-2 text-xs">
        <div>
          <span className="font-mono">{data.doc_a.title}</span>
          <span className="text-muted-foreground"> vs </span>
          <span className="font-mono">{data.doc_b.title}</span>
        </div>
        <div className="flex items-center gap-3 text-muted-foreground">
          <span className="text-emerald-500">+{data.summary.added_chars}</span>
          <span className="text-red-500">−{data.summary.removed_chars}</span>
          <span>{data.summary.changed_blocks} blocks</span>
          <Button variant="ghost" size="sm" onClick={onBack}>back</Button>
        </div>
      </header>
      {data.truncated && (
        <p className="rounded-md border border-amber-500/40 bg-amber-50/60 p-2 text-xs dark:bg-amber-950/20">
          Truncated to 200k chars per side — full diff is too large to render. See ADR 0101.
        </p>
      )}
      <div className="grid grid-cols-2 gap-3 overflow-hidden">
        <DiffPane title={data.doc_a.title} ops={left} side="a" />
        <DiffPane title={data.doc_b.title} ops={right} side="b" />
      </div>
    </div>
  )
}

function DiffPane({ title, ops, side }: { title: string; ops: DiffOperation[]; side: 'a' | 'b' }) {
  return (
    <section className="flex min-h-[40vh] flex-col overflow-hidden rounded-md border border-border bg-card">
      <header className="border-b border-border bg-muted/40 px-3 py-1.5 text-xs font-semibold">
        {title}
      </header>
      <div className="flex-1 overflow-y-auto p-3 font-mono text-xs leading-relaxed">
        {ops.map((o, i) => {
          if (o.op === 'equal') {
            return <span key={i} className="whitespace-pre-wrap">{o.text}</span>
          }
          if (o.op === 'delete' && side === 'a') {
            return (
              <span
                key={i}
                className="whitespace-pre-wrap bg-red-500/15 text-red-700 dark:text-red-300"
              >
                {o.text}
              </span>
            )
          }
          if (o.op === 'insert' && side === 'b') {
            return (
              <span
                key={i}
                className="whitespace-pre-wrap bg-emerald-500/15 text-emerald-700 dark:text-emerald-300"
              >
                {o.text}
              </span>
            )
          }
          return null
        })}
      </div>
    </section>
  )
}

function filterOps(ops: DiffOperation[], keepOp: 'insert' | 'delete'): DiffOperation[] {
  // Keep `equal` runs and the requested directional op; drop the other.
  return ops.filter((o) => o.op === 'equal' || o.op === keepOp)
}
