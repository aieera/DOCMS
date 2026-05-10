import { createFileRoute } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { toast } from 'sonner'
import { Download, Upload, AlertCircle, CheckCircle2 } from 'lucide-react'

import {
  streamImport,
  streamExport,
  type BulkResource,
  type BulkImportSummary,
  type BulkItemResult,
} from '@/api/bulk'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'
import { Card } from '@/components/ui/card'
import { LabeledSelect as Select } from '@/components/ui/shadcn/select'
import { Input } from '@/components/ui/shadcn/input'

// /admin/bulk — ADR 0075 import/export wizard. Two tabs in one card:
//   - Import: paste / drop NDJSON; live per-line status as the
//     server streams responses back.
//   - Export: pick a resource type + optional filters; downloads
//     NDJSON to disk via the standard <a download> trick.
//
// The wizard is intentionally minimal — no multi-step flow, no
// CSV→NDJSON conversion. Power users will scriptthe NDJSON
// generation against the proto schema and paste here, or hit the
// gRPC surface directly. The UI exists to make small migrations
// (a few hundred rows) straightforward without a CLI.

function BulkPage() {
  const [tab, setTab] = useState<'import' | 'export'>('import')
  return (
    <div className="space-y-6">
      <PageHeader
        title="Bulk import / export"
        description="Stream documents, workspaces, folders, users, and groups in or out via NDJSON. Designed for tenant migration off legacy DMSes."
      />

      <div className="inline-flex gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
        {(['import', 'export'] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={`rounded-sm px-3 py-1.5 transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              tab === t ? 'bg-background font-medium text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'
            }`}
            data-testid={`bulk-tab-${t}`}
          >
            {t === 'import' ? 'Import' : 'Export'}
          </button>
        ))}
      </div>

      {tab === 'import' ? <ImportPanel /> : <ExportPanel />}
    </div>
  )
}

function ImportPanel() {
  const [body, setBody] = useState('')
  const [running, setRunning] = useState(false)
  const [summary, setSummary] = useState<BulkImportSummary | null>(null)
  const [latest, setLatest] = useState<BulkItemResult[]>([])
  const abortRef = useRef<AbortController | null>(null)

  const onFile = async (f: File) => {
    const text = await f.text()
    setBody(text)
  }
  const onSubmit = async () => {
    if (!body.trim()) {
      toast.error('Paste or drop NDJSON before importing')
      return
    }
    setRunning(true)
    setSummary(null)
    setLatest([])
    const controller = new AbortController()
    abortRef.current = controller
    try {
      const sum = await streamImport(body, (r) => {
        setLatest((cur) => {
          // Keep only the last 50 entries on screen so a million-row
          // import doesn't kill the renderer.
          const next = [...cur, r]
          return next.length > 50 ? next.slice(next.length - 50) : next
        })
      }, controller.signal)
      setSummary(sum)
      toast.success(`Imported ${sum.successCount} of ${sum.totalLines}; ${sum.failureCount} failed`)
    } catch (e: unknown) {
      toast.error((e as Error).message || 'Import failed')
    } finally {
      setRunning(false)
      abortRef.current = null
    }
  }

  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">Import NDJSON</h2>
        <div className="flex items-center gap-2">
          <input
            type="file"
            accept=".ndjson,.jsonl,.json,application/x-ndjson"
            onChange={(e) => { const f = e.target.files?.[0]; if (f) void onFile(f) }}
            data-testid="bulk-import-file"
          />
          <Button
            onClick={() => void onSubmit()}
            disabled={running}
            loading={running}
            data-testid="bulk-import-run"
          >
            <Upload className="me-1 h-4 w-4" /> Run import
          </Button>
        </div>
      </div>

      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        spellCheck={false}
        rows={10}
        placeholder={`{"resource":"workspace","workspace":{"external_id":"W-1","name":"Legal","region_pin":"eu-west-1"}}\n{"resource":"document","document":{"external_id":"D-1","workspace_external_id":"W-1","title":"Contract.pdf","tags":["contract"]}}\n…`}
        className="h-64 w-full resize-y rounded-md border border-border bg-background p-2 font-mono text-xs"
        data-testid="bulk-import-textarea"
      />

      {summary && (
        <div className="grid grid-cols-3 gap-2 text-xs" data-testid="bulk-import-summary">
          <Card className="p-3"><span className="text-muted-foreground">Processed</span><p className="text-lg font-semibold">{summary.totalLines}</p></Card>
          <Card className="p-3"><span className="text-success">Succeeded</span><p className="text-lg font-semibold">{summary.successCount}</p></Card>
          <Card className="p-3"><span className="text-destructive">Failed</span><p className="text-lg font-semibold">{summary.failureCount}</p></Card>
        </div>
      )}

      {(latest.length > 0 || summary?.globalErrors.length) && (
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase text-muted-foreground">
            {summary ? 'Last 50 results' : 'Live results'}
          </p>
          <ul className="max-h-64 space-y-0.5 overflow-y-auto rounded border border-border bg-muted/40 p-2 font-mono text-[11px]" data-testid="bulk-import-results">
            {summary?.globalErrors.map((e, i) => (
              <li key={`g-${i}`} className="flex items-start gap-1 text-destructive">
                <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" /> {e}
              </li>
            ))}
            {latest.map((r, i) => (
              <li key={i} className={r.success ? 'text-success' : 'text-destructive'}>
                {r.success
                  ? <><CheckCircle2 className="me-1 inline h-3 w-3" />{r.skipped ? 'skip' : 'ok  '} {r.external_id ?? '?'} → {r.internal_id?.slice(0, 8) ?? ''}</>
                  : <><AlertCircle className="me-1 inline h-3 w-3" />fail {r.external_id ?? '?'} — {r.error}</>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  )
}

function ExportPanel() {
  const [resource, setResource] = useState<BulkResource>('document')
  const [workspaceId, setWorkspaceId] = useState('')
  const [running, setRunning] = useState(false)

  const onRun = async () => {
    setRunning(true)
    try {
      const items = await streamExport({
        resource,
        workspaceId: workspaceId.trim() || undefined,
      })
      const ndjson = items.map((it) => JSON.stringify(it)).join('\n') + '\n'
      const blob = new Blob([ndjson], { type: 'application/x-ndjson' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `vaultdms-${resource}-export-${new Date().toISOString().slice(0, 10)}.ndjson`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      toast.success(`Exported ${items.length} ${resource}${items.length === 1 ? '' : 's'}`)
    } catch (e: unknown) {
      toast.error((e as Error).message || 'Export failed')
    } finally {
      setRunning(false)
    }
  }

  return (
    <Card className="space-y-3 p-4">
      <h2 className="text-base font-semibold">Export NDJSON</h2>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Select
          label="Resource"
          value={resource}
          onValueChange={(v) => setResource(v as BulkResource)}
          options={[
            { value: 'workspace', label: 'Workspaces' },
            { value: 'folder',    label: 'Folders' },
            { value: 'document',  label: 'Documents' },
          ]}
        />
        {(resource === 'document' || resource === 'folder') && (
          <Input
            label="Workspace id (optional filter)"
            value={workspaceId}
            onChange={(e) => setWorkspaceId(e.target.value)}
            placeholder="00000000-0000-0000-0000-000000000000"
          />
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        User and group exports aren't implemented yet — those rows live in the auth service. Use the auth admin surface for now.
      </p>
      <Button onClick={() => void onRun()} disabled={running} loading={running} data-testid="bulk-export-run">
        <Download className="me-1 h-4 w-4" /> Export to file
      </Button>
    </Card>
  )
}

export const Route = createFileRoute('/_authenticated/admin/bulk')({ component: BulkPage })
