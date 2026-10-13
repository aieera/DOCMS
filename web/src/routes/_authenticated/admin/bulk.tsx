import { createFileRoute } from '@tanstack/react-router'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { Download, Upload, AlertCircle, CheckCircle2 } from 'lucide-react'

import {
  streamImport,
  streamExport,
  type BulkResource,
  type BulkImportSummary,
  type BulkItemResult,
} from '@/api/bulk'
import { UploadWizard } from '@/components/admin/bulkUpload/UploadWizard'
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

type BulkTab = 'upload' | 'advanced-import' | 'export'
// Keys, not text: a module-level t() freezes whichever locale loaded first.
const TAB_KEY: Record<BulkTab, string> = {
  upload: 'tab_upload',
  'advanced-import': 'tab_advanced',
  export: 'tab_export',
}

export function BulkPage() {
  const { t } = useTranslation('common')
  const [tab, setTab] = useState<BulkTab>('upload')
  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.bulk.title')}
        description={t('adm.bulk.description')}
      />

      <div className="inline-flex gap-1 rounded-md bg-muted/60 p-1 text-xs" role="tablist">
        {(['upload', 'advanced-import', 'export'] as const).map((tabKey) => (
          <button
            key={tabKey}
            type="button"
            role="tab"
            aria-selected={tab === tabKey}
            onClick={() => setTab(tabKey)}
            className={`rounded-sm px-3 py-1.5 transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
              tab === tabKey ? 'bg-background font-medium text-foreground shadow-neu-sm' : 'text-muted-foreground hover:text-foreground'
            }`}
            data-testid={`bulk-tab-${tabKey}`}
          >
            {t(`adm.bulk.${TAB_KEY[tabKey]}`)}
          </button>
        ))}
      </div>

      {tab === 'upload' && <UploadWizard />}
      {tab === 'advanced-import' && <ImportPanel />}
      {tab === 'export' && <ExportPanel />}
    </div>
  )
}

function ImportPanel() {
  const { t } = useTranslation('common')
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
      toast.error(t('adm.bulk.err_paste'))
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
      toast.success(t('adm.bulk.imported', { success: sum.successCount, total: sum.totalLines, failed: sum.failureCount }))
    } catch (e: unknown) {
      toast.error((e as Error).message || t('adm.bulk.err_import'))
    } finally {
      setRunning(false)
      abortRef.current = null
    }
  }

  return (
    <Card className="space-y-3 p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">{t('adm.bulk.import_title')}</h2>
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
            <Upload className="me-1 h-4 w-4" /> {t('adm.bulk.run_import')}
          </Button>
        </div>
      </div>

      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        spellCheck={false}
        rows={10}
        placeholder={`{"resource":"workspace","workspace":{"external_id":"W-1","name":"Legal","region_pin":"eu-west-1"}}\n{"resource":"document","document":{"external_id":"D-1","workspace_external_id":"W-1","title":"Contract.pdf","tags":["contract"]}}\n…`}
        className="h-64 w-full resize-y rounded-lg border border-input bg-muted p-2 font-mono text-xs shadow-neu-inset"
        data-testid="bulk-import-textarea"
      />

      {summary && (
        <div className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-3" data-testid="bulk-import-summary">
          <Card className="p-3"><span className="text-muted-foreground">{t('adm.bulk.processed')}</span><p className="text-lg font-semibold">{summary.totalLines}</p></Card>
          <Card className="p-3"><span className="text-success">{t('adm.bulk.succeeded')}</span><p className="text-lg font-semibold">{summary.successCount}</p></Card>
          <Card className="p-3"><span className="text-destructive">{t('adm.bulk.failed')}</span><p className="text-lg font-semibold">{summary.failureCount}</p></Card>
        </div>
      )}

      {(latest.length > 0 || summary?.globalErrors.length) && (
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase text-muted-foreground">
            {summary ? t('adm.bulk.last_50') : t('adm.bulk.live')}
          </p>
          <ul className="max-h-64 space-y-0.5 overflow-y-auto rounded-xl bg-muted p-2 font-mono text-[11px] shadow-neu-inset" data-testid="bulk-import-results">
            {summary?.globalErrors.map((e, i) => (
              <li key={`g-${i}`} className="flex items-start gap-1 text-destructive">
                <AlertCircle className="mt-0.5 h-3 w-3 shrink-0" /> {e}
              </li>
            ))}
            {latest.map((r, i) => (
              <li key={i} className={r.success ? 'text-success' : 'text-destructive'}>
                {r.success
                  ? <><CheckCircle2 className="me-1 inline h-3 w-3" />{r.skipped ? t('adm.bulk.skip') : t('adm.bulk.ok')} {r.external_id ?? '?'} → {r.internal_id?.slice(0, 8) ?? ''}</>
                  : <><AlertCircle className="me-1 inline h-3 w-3" />{t('adm.bulk.fail')} {r.external_id ?? '?'} — {r.error}</>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </Card>
  )
}

function ExportPanel() {
  const { t } = useTranslation('common')
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
      toast.success(t(`adm.bulk.exported_${resource}`, { count: items.length }))
    } catch (e: unknown) {
      toast.error((e as Error).message || t('adm.bulk.err_export'))
    } finally {
      setRunning(false)
    }
  }

  return (
    <Card className="space-y-3 p-4">
      <h2 className="text-base font-semibold">{t('adm.bulk.export_title')}</h2>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Select
          label={t('adm.bulk.resource')}
          value={resource}
          onValueChange={(v) => setResource(v as BulkResource)}
          options={[
            { value: 'workspace', label: t('adm.bulk.r_workspace') },
            { value: 'folder',    label: t('adm.bulk.r_folder') },
            { value: 'document',  label: t('adm.bulk.r_document') },
          ]}
        />
        {(resource === 'document' || resource === 'folder') && (
          <Input
            label={t('adm.bulk.ws_filter')}
            dir="ltr"
            value={workspaceId}
            onChange={(e) => setWorkspaceId(e.target.value)}
            placeholder="00000000-0000-0000-0000-000000000000"
          />
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        {t('adm.bulk.export_note')}
      </p>
      <Button onClick={() => void onRun()} disabled={running} loading={running} data-testid="bulk-export-run">
        <Download className="me-1 h-4 w-4" /> {t('adm.bulk.export_btn')}
      </Button>
    </Card>
  )
}

export const Route = createFileRoute('/_authenticated/admin/bulk')({ component: BulkPage })
