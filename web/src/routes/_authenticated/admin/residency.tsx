import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { createFileRoute, redirect } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { toast } from 'sonner'
import { AlertTriangle, CheckCircle2, Globe } from 'lucide-react'
import {
  createResidencyMigration,
  getResidencyStats,
  listResidencyMigrations,
  type ResidencyMigration,
} from '@/api/residency'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

// ADR 0110 — read the cluster's region from any service's /healthz.
// Document service is the canonical owner of the residency surface so
// we ping its health port directly. Falls back to 'unknown' if the
// fetch fails — the banner downgrades to a yellow "couldn't verify"
// state rather than misleading green.
async function fetchClusterRegion(): Promise<string> {
  try {
    const res = await fetch('/healthz', { credentials: 'include' })
    if (!res.ok) return 'unknown'
    const body = (await res.json()) as { region?: string }
    return (body.region ?? 'unknown').toLowerCase()
  } catch {
    return 'unknown'
  }
}

export function ResidencyPage() {
  const { t } = useTranslation('common')
  const qc = useQueryClient()
  const stats = useQuery({ queryKey: ['residency-stats'], queryFn: getResidencyStats })
  const migrations = useQuery({ queryKey: ['residency-migrations'], queryFn: listResidencyMigrations })
  const cluster = useQuery({
    queryKey: ['cluster-region'],
    queryFn: fetchClusterRegion,
    staleTime: 5 * 60_000,
  })

  const [src, setSrc] = useState('')
  const [tgt, setTgt] = useState('')

  const submit = useAppMutation({
    mutationFn: () => createResidencyMigration({ source_region: src, target_region: tgt }),
    onSuccess: () => {
      toast.success(t('adm.res.queued'))
      setSrc('')
      setTgt('')
      qc.invalidateQueries({ queryKey: ['residency-migrations'] })
    },
    onError: () => toast.error(t('adm.res.err_queue')),
  })

  return (
    <div>
      <PageHeader
        title={t('adm.res.title')}
        description={t('adm.res.description')}
      />

      <ResidencyBanner clusterRegion={cluster.data} statRegions={(stats.data ?? []).map((r) => r.region)} />

      <h3 className="mb-2 mt-2 flex items-center gap-2 font-medium">
        <Globe className="h-4 w-4" /> {t('adm.res.distribution')}
      </h3>
      {stats.isLoading ? (
        <Skeleton className="h-16" />
      ) : !stats.data || stats.data.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('adm.res.no_docs')}</p>
      ) : (
        <div className="mb-6 overflow-hidden rounded-2xl bg-card shadow-sm">
          <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="bg-muted/40">
              <tr className="text-start">
                <th className="px-4 py-2">{t('adm.res.col_region')}</th>
                <th className="px-4 py-2">{t('adm.res.col_documents')}</th>
                <th className="px-4 py-2">{t('adm.res.col_storage')}</th>
              </tr>
            </thead>
            <tbody>
              {stats.data.map((row) => (
                <tr key={row.region} className="border-t border-border">
                  <td dir="ltr" className="px-4 py-2 font-medium">{row.region}</td>
                  <td className="px-4 py-2">{row.doc_count.toLocaleString()}</td>
                  <td className="px-4 py-2">{formatFileSize(row.blob_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        </div>
      )}

      <div className="mb-6 rounded-lg bg-card p-4 shadow-sm">
        <h3 className="mb-3 flex items-center gap-2 font-medium">
          <DirectionalIcon name="ArrowRight" className="h-4 w-4" /> {t('adm.res.migrate_docs')}
        </h3>
        {/* See admin/privacy: 1fr floors at min-content and grid items
            default to min-width:auto, so the region fields shoved the
            Migrate button out of the card once Arabic widened the row. */}
        <div className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)_auto] items-center gap-3 [&>*]:min-w-0">
          <input
            className="rounded-md border border-input bg-muted px-2 py-1 text-sm"
            dir="ltr"
            placeholder={t('adm.res.src_ph')}
            value={src}
            onChange={(e) => setSrc(e.target.value)}
          />
          <DirectionalIcon name="ArrowRight" className="h-4 w-4 text-muted-foreground" />
          <input
            className="rounded-md border border-input bg-muted px-2 py-1 text-sm"
            dir="ltr"
            placeholder={t('adm.res.tgt_ph')}
            value={tgt}
            onChange={(e) => setTgt(e.target.value)}
          />
          <Button
            onClick={() => {
              if (!src.trim()) { toast.error(t('adm.res.err_src')); return }
              if (!tgt.trim()) { toast.error(t('adm.res.err_tgt')); return }
              if (src.trim() === tgt.trim()) { toast.error(t('adm.res.err_same')); return }
              submit.mutate()
            }}
            disabled={submit.isPending}
          >
            {t('adm.res.migrate')}
          </Button>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          {t('adm.res.migrate_hint')}
        </p>
      </div>

      <h3 className="mb-2 font-medium">{t('adm.res.recent')}</h3>
      {migrations.isLoading ? (
        <Skeleton className="h-20" />
      ) : !migrations.data || migrations.data.length === 0 ? (
        <EmptyState
          icon={<Globe className="h-12 w-12" />}
          title={t('adm.res.empty')}
          description={t('adm.res.empty_desc')}
        />
      ) : (
        <ul className="space-y-2">
          {migrations.data.map((m) => (
            <li
              key={m.id}
              className="rounded-lg bg-card p-3 shadow-sm"
            >
              <div className="flex items-center gap-2">
                <span dir="ltr" className="font-medium">
                  {m.source_region} → {m.target_region}
                </span>
                <Badge variant={migStatus(m.status)}>{t(`adm.res.st_${m.status}`)}</Badge>
                <span className="ms-auto text-xs text-muted-foreground">
                  {formatRelativeTime(m.created_at)}
                </span>
              </div>
              <div dir="auto" className="mt-1 text-xs text-muted-foreground">
                {t('adm.res.progress', { moved: m.moved_docs, total: m.total_docs, failed: m.failed_docs })}
              </div>
              {m.error_summary && <p className="mt-1 text-xs text-destructive">{m.error_summary}</p>}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

// ResidencyBanner — ADR 0110 cluster-vs-tenant residency status.
//
// Three states, chosen by comparing /healthz `region` to the
// regions present in the tenant's document distribution:
//
//   * green  — cluster region serves at least one document and
//     every document lives in the cluster's region (the happy path).
//   * red    — cluster region differs from one or more rows in the
//     distribution table. That's a residency drift; documents
//     should not live outside their tenant's region.
//   * amber  — couldn't determine the cluster region (fetch failed
//     or service didn't expose it), OR there are zero documents
//     yet (so we have no signal to compare against).
function ResidencyBanner({
  clusterRegion,
  statRegions,
}: {
  clusterRegion: string | undefined
  statRegions: string[]
}) {
  const { t } = useTranslation('common')
  if (!clusterRegion || clusterRegion === 'unknown') {
    return (
      <div className="mb-4 flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-3 text-sm">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning" />
        <div>
          <p className="font-medium text-warning-strong">{t('adm.res.unknown_title')}</p>
          <p className="text-xs text-warning-strong/80">
            {/* <Trans>: two literal identifiers sit mid-sentence and Arabic
                reorders the clauses around them. Both stay dir="ltr". */}
            <Trans
              i18nKey="adm.res.unknown_body"
              ns="common"
              components={[<code key="p" dir="ltr" />, <code key="e" dir="ltr" />]}
            />
          </p>
        </div>
      </div>
    )
  }
  const drift = statRegions.filter((r) => r.toLowerCase() !== clusterRegion)
  if (drift.length === 0) {
    return (
      <div className="mb-4 flex items-start gap-2 rounded-md border border-success/40 bg-success/10 p-3 text-sm">
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-success" />
        <div>
          <p className="font-medium text-success">
            <Trans
              i18nKey="adm.res.ok_title"
              ns="common"
              values={{ region: clusterRegion }}
              components={[<code key="r" dir="ltr" />]}
            />
          </p>
          <p className="text-xs text-success/80">{t('adm.res.ok_body')}</p>
        </div>
      </div>
    )
  }
  return (
    <div className="mb-4 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
      <div>
        <p className="font-medium text-destructive">
          <Trans
            i18nKey="adm.res.drift_title"
            ns="common"
            values={{ region: clusterRegion, others: drift.join(', ') }}
            components={[<code key="r" dir="ltr" />]}
          />
        </p>
        <p className="text-xs text-destructive/80">
          <Trans
            i18nKey="adm.res.drift_body"
            ns="common"
            values={{ region: clusterRegion }}
            components={[<code key="r" dir="ltr" />]}
          />
        </p>
      </div>
    </div>
  )
}

function migStatus(s: ResidencyMigration['status']): string {
  switch (s) {
    case 'completed':
      return 'active'
    case 'failed':
    case 'cancelled':
      return 'disposed'
    case 'running':
    case 'pending':
      return 'in_review'
    default:
      return 'default'
  }
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/data-governance?tab=residency). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/residency')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/data-governance', search: { tab: 'residency' }, replace: true })
  },
})
