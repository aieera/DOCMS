import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import toast from 'react-hot-toast'
import { Globe, ArrowRight } from 'lucide-react'

import {
  createResidencyMigration,
  getResidencyStats,
  listResidencyMigrations,
  type ResidencyMigration,
} from '@/api/residency'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Badge } from '@/components/ui/Badge'
import { Button } from '@/components/ui/shadcn/button'
import { Skeleton } from '@/components/ui/Skeleton'
import { formatFileSize, formatRelativeTime } from '@/lib/formatters'

function ResidencyPage() {
  const qc = useQueryClient()
  const stats = useQuery({ queryKey: ['residency-stats'], queryFn: getResidencyStats })
  const migrations = useQuery({ queryKey: ['residency-migrations'], queryFn: listResidencyMigrations })

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
              <tr className="text-left">
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
          <ArrowRight className="h-4 w-4" /> Migrate documents
        </h3>
        <div className="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-3">
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="source region (e.g. us-east-1)"
            value={src}
            onChange={(e) => setSrc(e.target.value)}
          />
          <ArrowRight className="h-4 w-4 text-muted-foreground" />
          <input
            className="rounded-md border border-border bg-background px-2 py-1 text-sm"
            placeholder="target region (e.g. eu-west-1)"
            value={tgt}
            onChange={(e) => setTgt(e.target.value)}
          />
          <Button
            onClick={() => submit.mutate()}
            disabled={!src || !tgt || src === tgt || submit.isPending}
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
                <span className="ml-auto text-xs text-muted-foreground">
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
