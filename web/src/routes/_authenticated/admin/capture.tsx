// Scan-capture: upload a multi-page scan bundle, preview the proposed barcode
// split, correct boundaries (merge/split), map barcode→metadata, then commit —
// each segment becomes one document via the ingestion pipeline.
import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { toast } from 'sonner'

import { useWorkspaces } from '@/hooks/useWorkspaces'
import { useFolders } from '@/hooks/useFolders'
import { Button } from '@/components/ui/shadcn/button'
import { analyzeBundle, commitBundle, type CaptureAnalysis } from '@/api/capture'

function fileToB64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const fr = new FileReader()
    fr.onerror = () => reject(fr.error)
    fr.onload = () => {
      const s = String(fr.result)
      resolve(s.slice(s.indexOf(',') + 1)) // strip the data:…;base64, prefix
    }
    fr.readAsDataURL(file)
  })
}

function CapturePage() {
  const [bundleB64, setBundleB64] = useState('')
  const [mime, setMime] = useState('application/pdf')
  const [mode, setMode] = useState<'separator_sheet' | 'zonal'>('separator_sheet')
  const [pattern, setPattern] = useState('')
  const [analysis, setAnalysis] = useState<CaptureAnalysis | null>(null)
  // groups / titles / metaKeys are kept index-parallel through merge/split.
  const [groups, setGroups] = useState<number[][]>([])
  const [titles, setTitles] = useState<string[]>([])
  const [metaKeys, setMetaKeys] = useState<string[]>([])
  const [wsId, setWsId] = useState('')
  const [folderId, setFolderId] = useState('')
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<number | null>(null)

  const workspaces = useWorkspaces()
  const folders = useFolders(wsId)

  const onPick = async (f: File | null) => {
    setAnalysis(null)
    setResult(null)
    if (!f) {
      setBundleB64('')
      return
    }
    const lower = f.name.toLowerCase()
    setMime(f.type || (lower.endsWith('.tif') || lower.endsWith('.tiff') ? 'image/tiff' : 'application/pdf'))
    setBundleB64(await fileToB64(f))
  }

  const onAnalyze = async () => {
    if (!bundleB64) return
    setBusy(true)
    setResult(null)
    try {
      const a = await analyzeBundle({ bundle_b64: bundleB64, mime, mode, separator_pattern: pattern || undefined })
      setAnalysis(a)
      setGroups(a.segments.map((s) => s.pages))
      setTitles(a.segments.map(() => ''))
      setMetaKeys(a.segments.map(() => ''))
    } catch (e) {
      toast.error((e as Error).message ?? 'Analyze failed')
    } finally {
      setBusy(false)
    }
  }

  const barcodeOf = (group: number[]): string | null => analysis?.page_barcodes[group[0]]?.[0] ?? null

  const mergeUp = (gi: number) => {
    if (gi === 0) return
    setGroups((g) => {
      const n = g.map((x) => [...x])
      n[gi - 1] = [...n[gi - 1], ...n[gi]]
      n.splice(gi, 1)
      return n
    })
    setTitles((t) => { const n = [...t]; n.splice(gi, 1); return n })
    setMetaKeys((m) => { const n = [...m]; n.splice(gi, 1); return n })
  }

  const splitAt = (gi: number, idx: number) => {
    if (idx <= 0) return
    setGroups((g) => {
      const n = g.map((x) => [...x])
      const grp = n[gi]
      n.splice(gi, 1, grp.slice(0, idx), grp.slice(idx))
      return n
    })
    setTitles((t) => { const n = [...t]; n.splice(gi, 1, n[gi], ''); return n })
    setMetaKeys((m) => { const n = [...m]; n.splice(gi, 1, n[gi], ''); return n })
  }

  const onCommit = async () => {
    if (!analysis || !wsId || !folderId || groups.length === 0) return
    setBusy(true)
    try {
      const segments = groups.map((pages, gi) => {
        const bc = barcodeOf(pages)
        const title = titles[gi] || (bc ? `Document ${gi + 1} [${bc}]` : `Document ${gi + 1}`)
        const metadata: Record<string, unknown> = metaKeys[gi] && bc ? { [metaKeys[gi]]: bc } : {}
        return { title, metadata }
      })
      const res = await commitBundle({ bundle_b64: bundleB64, mime, page_groups: groups, workspace_id: wsId, folder_id: folderId, segments })
      setResult(res.count)
      toast.success(`Created ${res.count} document${res.count === 1 ? '' : 's'}`)
    } catch (e) {
      toast.error((e as Error).message ?? 'Commit failed')
    } finally {
      setBusy(false)
    }
  }

  const canCommit = !!analysis && !!wsId && !!folderId && groups.length > 0 && !busy

  return (
    <div className="mx-auto max-w-5xl space-y-6 p-6">
      <header>
        <h1 className="text-xl font-bold">Scan capture</h1>
        <p className="text-sm text-muted-foreground">
          Upload a multi-page scan (PDF/TIFF). Pages are split into separate documents at barcode / patch-code boundaries — review and correct before committing.
        </p>
      </header>

      {/* upload + mode */}
      <div className="space-y-3 rounded-lg border p-4">
        <input
          type="file"
          accept="application/pdf,image/tiff"
          onChange={(e) => void onPick(e.target.files?.[0] ?? null)}
          data-testid="capture-file"
        />
        <div className="flex flex-wrap items-end gap-3">
          <label className="text-sm">
            Separation mode
            <select
              className="ms-2 rounded border px-2 py-1"
              value={mode}
              onChange={(e) => setMode(e.target.value as 'separator_sheet' | 'zonal')}
            >
              <option value="separator_sheet">Separator sheets</option>
              <option value="zonal">Zonal (barcode starts a doc)</option>
            </select>
          </label>
          <label className="text-sm">
            Separator pattern (regex, optional)
            <input
              className="ms-2 rounded border px-2 py-1"
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              placeholder="^SEP-"
            />
          </label>
          <Button onClick={() => void onAnalyze()} disabled={!bundleB64 || busy}>
            {busy && !analysis ? 'Analyzing…' : 'Analyze split'}
          </Button>
        </div>
      </div>

      {/* split preview + correction */}
      {analysis && (
        <div className="space-y-4">
          <div className="flex items-center justify-between">
            <h2 className="font-semibold">
              Proposed split — {groups.length} document{groups.length === 1 ? '' : 's'} from {analysis.page_count} pages
            </h2>
          </div>

          {groups.map((pages, gi) => {
            const bc = barcodeOf(pages)
            return (
              <div key={gi} className="rounded-lg border p-3" data-testid={`segment-${gi}`}>
                <div className="mb-2 flex flex-wrap items-center gap-2">
                  <input
                    className="rounded border px-2 py-1 text-sm font-medium"
                    value={titles[gi]}
                    onChange={(e) => setTitles((t) => { const n = [...t]; n[gi] = e.target.value; return n })}
                    placeholder={bc ? `Document ${gi + 1} [${bc}]` : `Document ${gi + 1}`}
                  />
                  {bc && <span className="rounded bg-muted px-2 py-0.5 text-xs">barcode: {bc}</span>}
                  {bc && (
                    <input
                      className="rounded border px-2 py-1 text-xs"
                      value={metaKeys[gi]}
                      onChange={(e) => setMetaKeys((m) => { const n = [...m]; n[gi] = e.target.value; return n })}
                      placeholder="map barcode → metadata key"
                    />
                  )}
                  {gi > 0 && (
                    <Button variant="outline" size="sm" onClick={() => mergeUp(gi)}>Merge ↑</Button>
                  )}
                </div>
                <div className="flex flex-wrap gap-2">
                  {pages.map((p, idx) => (
                    <div key={p} className="flex flex-col items-center gap-1">
                      {idx > 0 && (
                        <button
                          type="button"
                          className="text-[10px] text-primary hover:underline"
                          onClick={() => splitAt(gi, idx)}
                          title="Split into a new document starting at this page"
                        >
                          ✂ split before
                        </button>
                      )}
                      <img
                        src={`data:image/png;base64,${analysis.page_thumbnails[p] ?? ''}`}
                        alt={`page ${p + 1}`}
                        className="h-28 w-auto rounded border"
                      />
                      <span className="text-[10px] text-muted-foreground">p{p + 1}</span>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}

          {/* destination + commit */}
          <div className="flex flex-wrap items-end gap-3 rounded-lg border p-4">
            <label className="text-sm">
              Workspace
              <select className="ms-2 rounded border px-2 py-1" value={wsId} onChange={(e) => { setWsId(e.target.value); setFolderId('') }}>
                <option value="">Pick…</option>
                {(workspaces.data ?? []).map((w) => <option key={w.id} value={w.id}>{w.name}</option>)}
              </select>
            </label>
            <label className="text-sm">
              Folder
              <select className="ms-2 rounded border px-2 py-1" value={folderId} onChange={(e) => setFolderId(e.target.value)} disabled={!wsId}>
                <option value="">Pick…</option>
                {(folders.data ?? []).map((f) => <option key={f.id} value={f.id}>{f.name}</option>)}
              </select>
            </label>
            <Button onClick={() => void onCommit()} disabled={!canCommit}>
              {busy && analysis ? 'Committing…' : `Create ${groups.length} document${groups.length === 1 ? '' : 's'}`}
            </Button>
          </div>

          {result != null && (
            <div className="rounded-md border border-emerald-300 bg-emerald-50 p-3 text-sm text-emerald-800" data-testid="capture-result">
              Created {result} document{result === 1 ? '' : 's'} from the scan. OCR + classification run in the background.
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/capture')({ component: CapturePage })
