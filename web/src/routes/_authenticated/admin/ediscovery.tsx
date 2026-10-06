import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import i18n from '@/i18n'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Search, FileArchive, Download, Loader2, AlertTriangle } from 'lucide-react'

import { listHolds } from '@/api/holds'
import {
  getScope, createExportJob, listExportJobs, downloadExportJob,
  type HoldScope, type ExportJob,
} from '@/api/ediscovery'
import { useAppMutation } from '@/hooks/useAppMutation'
import { PageHeader } from '@/components/shared/PageHeader'
import { Button } from '@/components/ui/shadcn/button'

const unit = (key: string) => i18n.t(`unit.${key}`, { ns: 'common', defaultValue: key.toUpperCase() })

function humanSize(bytes: number): string {
  // LRI…PDI bidi-isolates the "<number> <unit>" pair so RTL text around it
  // cannot reorder the two, matching lib/formatters' formatFileSize.
  const iso = (n: string, u: string) => `\u2066${n}\u00A0${u}\u2069`
  if (bytes < 1024) return iso(String(bytes), unit('b'))
  const units = ['kb', 'mb', 'gb', 'tb']
  let v = bytes / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return iso(v.toFixed(1), unit(units[i]))
}

export function EDiscoveryPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const { data: holds = [] } = useQuery({ queryKey: ['holds', 'active'], queryFn: () => listHolds({ status: 'active' }) })
  const [holdId, setHoldId] = useState('')
  const [query, setQuery] = useState('')
  const [scope, setScope] = useState<HoldScope | null>(null)

  // Poll the jobs list while any job is running so progress + the download
  // link appear without a manual refresh.
  const { data: jobs = [] } = useQuery({
    queryKey: ['export-jobs'],
    queryFn: listExportJobs,
    refetchInterval: (q) => {
      const data = q.state.data as ExportJob[] | undefined
      return data?.some((j) => j.status === 'pending' || j.status === 'running') ? 2000 : false
    },
  })

  const preview = useAppMutation({
    mutationFn: () => getScope(holdId, query),
    onSuccess: setScope,
    defaultErrorMessage: t('adm.edisc.err_preview'),
  })

  const request = useAppMutation({
    mutationFn: () => createExportJob(holdId, query),
    onSuccess: () => {
      toast.success(t('adm.edisc.requested'))
      qc.invalidateQueries({ queryKey: ['export-jobs'] })
    },
    defaultErrorMessage: t('adm.edisc.err_request'),
  })

  const download = useAppMutation({
    mutationFn: (id: string) => downloadExportJob(id),
    defaultErrorMessage: t('adm.edisc.err_download'),
  })

  return (
    <div className="space-y-6">
      <PageHeader
        noMargin
        title={t('adm.edisc.title')}
        description={t('adm.edisc.description')}
      />

      <div className="space-y-3 rounded-lg border border-border bg-card p-4">
        <div className="grid gap-2 sm:grid-cols-2">
          <div>
            <label className="text-xs font-semibold uppercase text-muted-foreground">{t('adm.edisc.hold_label')}</label>
            <select
              className="mt-1 w-full rounded border border-border bg-background px-2 py-1.5 text-sm"
              value={holdId}
              onChange={(e) => { setHoldId(e.target.value); setScope(null) }}
              data-testid="hold-select"
            >
              <option value="">{t('adm.edisc.hold_ph')}</option>
              {holds.map((h) => (
                <option key={h.id} value={h.id}>{t('adm.edisc.hold_option', { name: h.name, count: h.document_ids?.length ?? 0 })}</option>
              ))}
            </select>
          </div>
          <div>
            <label className="text-xs font-semibold uppercase text-muted-foreground">{t('adm.edisc.refine_label')}</label>
            <input
              className="mt-1 w-full rounded border border-border bg-background px-2 py-1.5 text-sm"
              placeholder={t('adm.edisc.refine_ph')}
              value={query}
              onChange={(e) => { setQuery(e.target.value); setScope(null) }}
              data-testid="refine-query"
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <Button size="sm" variant="outline" disabled={!holdId || preview.isPending}
            onClick={() => preview.mutate(undefined)} data-testid="preview-scope">
            <Search className="h-3.5 w-3.5" /> {t('adm.edisc.preview')}
          </Button>
          {scope && (
            <span className="rounded bg-muted px-2 py-1 text-sm" data-testid="scope-summary">
              <Trans
                i18nKey="adm.edisc.scope"
                ns="common"
                count={scope.doc_count}
                values={{ total: scope.doc_count, size: humanSize(scope.size_bytes) }}
                components={[<strong key="n" />]}
              />
            </span>
          )}
          <Button size="sm" className="ms-auto" disabled={!holdId || request.isPending}
            onClick={() => request.mutate(undefined)} data-testid="request-export">
            <FileArchive className="h-3.5 w-3.5" /> {t('adm.edisc.request')}
          </Button>
        </div>
      </div>

      <div>
        <h3 className="mb-2 text-sm font-semibold">{t('adm.edisc.jobs')}</h3>
        <div className="space-y-2">
        {jobs.map((j) => (
          <div key={j.id} className="flex flex-wrap items-center gap-3 rounded-lg border border-border p-3 text-sm" data-testid={`job-${j.id}`}>
            <JobStatusIcon status={j.status} />
            <span className="font-mono text-xs">{j.id.slice(0, 8)}</span>
            <span className="rounded bg-muted px-1.5 py-0.5 text-xs">{t(`adm.edisc.st_${j.status}`)}</span>
            <span className="text-muted-foreground">{t('adm.edisc.docs_size', { count: j.doc_count, size: humanSize(j.size_bytes) })}</span>
            {j.query && <span className="text-xs text-muted-foreground">“{j.query}”</span>}
            {j.status === 'failed' && j.error && (
              <span className="text-xs text-destructive" title={j.error}>{j.error.slice(0, 60)}</span>
            )}
            {j.download_ready && (
              <Button size="sm" variant="outline" className="ms-auto h-7" disabled={download.isPending}
                onClick={() => download.mutate(j.id)} data-testid={`download-${j.id}`}>
                <Download className="h-3.5 w-3.5" /> {t('adm.edisc.download')}
              </Button>
            )}
          </div>
        ))}
        {jobs.length === 0 && (
          <p className="rounded border border-dashed border-border p-4 text-center text-sm text-muted-foreground">
            {t('adm.edisc.empty')}
          </p>
        )}
        </div>
      </div>
    </div>
  )
}

function JobStatusIcon({ status }: { status: ExportJob['status'] }) {
  if (status === 'completed') return <FileArchive className="h-4 w-4 text-success" />
  if (status === 'failed') return <AlertTriangle className="h-4 w-4 text-destructive" />
  return <Loader2 className="h-4 w-4 animate-spin text-primary" />
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/legal?tab=ediscovery). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/ediscovery')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/legal', search: { tab: 'ediscovery' }, replace: true })
  },
})
