import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
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

  const submit = useMutation({
    mutationFn: () => createResidencyMigration({ source_region: src, target_region: tgt }),
    onSuccess: () => {
      toast.success('Migration queued')
      setSrc('')
      setTgt('')
      qc.invalidateQueries({ queryKey: ['residency-migrations'] })
    },
    onError: () => toast.error('Failed to queue migration'),
  })

  return (
    <div>
      <PageHeader
        title="Data Residency"
        description="Where your documents live, and how to move them."
      />

      <ResidencyBanner clusterRegion={cluster.data} statRegions={(stats.data ?? []).map((r) => r.region)} />

      <h3 className="mb-2 mt-2 flex items-center gap-2 font-medium">
        <Globe className="h-4 w-4" /> Distribution
      </h3>
      {stats.isLoading ? (
        <Skeleton className="h-16" />
      ) : !stats.data || stats.data.length === 0 ? (
        <p className="text-sm text-muted-foreground">No documents yet.</p>
      ) : (
        <div className="mb-6 overflow-hidden rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40">
              <tr className="text-start">
                <th className="px-4 py-2">Region</th>
                <th className="px-4 py-2">Documents</th>
                <th className="px-4 py-2">Storage used</th>
              </tr>
            </thead>
            <tbody>
              {stats.data.map((row) => (
                <tr key={row.region} className="border-t border-border">
                  <td className="px-4 py-2 font-medium">{row.region}</td>
                  <td className="px-4 py-2">{row.doc_count.toLocaleString()}</td>
                  <td className="px-4 py-2">{formatFileSize(row.blob_bytes)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="mb-6 rounded-lg border border-border bg-card p-4">
        <h3 className="mb-3 flex items-center gap-2 font-medium">
          <DirectionalIcon name="ArrowRight" className="h-4 w-4" /> Migrate documents
        </h3>
        <div className="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-3">
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="source region (e.g. us-east-1)"
            value={src}
            onChange={(e) => setSrc(e.target.value)}
          />
          <DirectionalIcon name="ArrowRight" className="h-4 w-4 text-muted-foreground" />
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="target region (e.g. eu-west-1)"
            value={tgt}
            onChange={(e) => setTgt(e.target.value)}
          />
          <Button
            onClick={() => {
              if (!src.trim()) { toast.error('Source region is required'); return }
              if (!tgt.trim()) { toast.error('Target region is required'); return }
              if (src.trim() === tgt.trim()) { toast.error('Source and target regions must differ'); return }
              submit.mutate()
            }}
            disabled={submit.isPending}
          >
            Migrate
          </Button>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          Queues a resumable Temporal workflow. Blob ciphertext is rekeyed under the region-local KEK
          in the storage service's copy step (Wave 12 — today the workflow flips `region_pin` and
          emits `dms.residency.migrated.v1` per document).
        </p>
      </div>

      <h3 className="mb-2 font-medium">Recent migrations</h3>
      {migrations.isLoading ? (
        <Skeleton className="h-20" />
      ) : !migrations.data || migrations.data.length === 0 ? (
        <EmptyState
          icon={<Globe className="h-12 w-12" />}
          title="No migrations yet"
          description="Submit one above to move documents between regions."
        />
      ) : (
        <ul className="space-y-2">
          {migrations.data.map((m) => (
            <li
              key={m.id}
              className="rounded-lg border border-border bg-card p-3"
            >
              <div className="flex items-center gap-2">
                <span className="font-medium">
                  {m.source_region} → {m.target_region}
                </span>
                <Badge variant={migStatus(m.status)}>{m.status}</Badge>
                <span className="ms-auto text-xs text-muted-foreground">
                  {formatRelativeTime(m.created_at)}
                </span>
              </div>
              <div className="mt-1 text-xs text-muted-foreground">
                {m.moved_docs} / {m.total_docs} moved · {m.failed_docs} failed
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
  if (!clusterRegion || clusterRegion === 'unknown') {
    return (
      <div className="mb-4 flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-50/60 p-3 text-sm dark:bg-amber-950/30">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
        <div>
          <p className="font-medium text-amber-900 dark:text-amber-200">Cluster region unknown</p>
          <p className="text-xs text-amber-800 dark:text-amber-300">
            <code>/healthz</code> did not return a region. Confirm the service was started with <code>VAULTDMS_REGION_ID</code>.
          </p>
        </div>
      </div>
    )
  }
  const drift = statRegions.filter((r) => r.toLowerCase() !== clusterRegion)
  if (drift.length === 0) {
    return (
      <div className="mb-4 flex items-start gap-2 rounded-md border border-emerald-500/40 bg-emerald-50/60 p-3 text-sm dark:bg-emerald-950/30">
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-600" />
        <div>
          <p className="font-medium text-emerald-900 dark:text-emerald-200">
            Residency OK — cluster region <code>{clusterRegion}</code>
          </p>
          <p className="text-xs text-emerald-800 dark:text-emerald-300">
            Every document lives in this region. New uploads will be pinned here.
          </p>
        </div>
      </div>
    )
  }
  return (
    <div className="mb-4 flex items-start gap-2 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm">
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
      <div>
        <p className="font-medium text-destructive">
          Residency drift — cluster runs in <code>{clusterRegion}</code> but documents exist in {drift.join(', ')}
        </p>
        <p className="text-xs text-destructive/80">
          Move them with the migration tool below. New uploads to this cluster will pin to{' '}
          <code>{clusterRegion}</code>; the existing rows must be migrated explicitly.
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

export const Route = createFileRoute('/_authenticated/admin/residency')({ component: ResidencyPage })
