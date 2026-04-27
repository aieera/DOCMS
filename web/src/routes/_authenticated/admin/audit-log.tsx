import { createFileRoute } from '@tanstack/react-router'
import { useQuery, useMutation } from '@tanstack/react-query'
import { Download, ShieldCheck, AlertTriangle } from 'lucide-react'
import { useState } from 'react'
import toast from 'react-hot-toast'

import { getAuditLog, exportAuditCSV, verifyAuditIntegrity, type AuditIntegrityResult } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { AuditLogTable } from '@/components/admin/AuditLogTable'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'

function AuditLogPage() {
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'audit-log'], queryFn: () => getAuditLog() })
  const [integrity, setIntegrity] = useState<AuditIntegrityResult | null>(null)

  const verify = useMutation({
    mutationFn: verifyAuditIntegrity,
    onSuccess: (r) => {
      setIntegrity(r)
      if (r.ok) toast.success(`Chain verified · ${r.verified_count} events`)
      else toast.error('Integrity check failed — see details above')
    },
    onError: () => toast.error('Verification request failed'),
  })

  const handleExport = async () => {
    try {
      await exportAuditCSV()
      toast.success('Audit log downloaded')
    } catch {
      toast.error('Export failed')
    }
  }

  return (
    <div>
      <PageHeader
        title="Audit Log"
        description="Activity history across the tenant. Every entry is SHA-256 chained — the 'Verify chain' button recomputes the chain on demand."
        actions={
          <div className="flex items-center gap-2">
            <Button
              variant="ghost"
              onClick={() => verify.mutate()}
              loading={verify.isPending}
              data-testid="audit-verify"
            >
              <ShieldCheck className="mr-1 h-4 w-4" /> Verify chain
            </Button>
            <Button variant="ghost" onClick={handleExport} data-testid="audit-export">
              <Download className="mr-1 h-4 w-4" /> Export CSV
            </Button>
          </div>
        }
      />

      {integrity && (
        <div
          role="status"
          data-testid="audit-integrity-result"
          className={
            integrity.ok
              ? 'mt-4 flex items-start gap-2 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-sm text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200'
              : 'mt-4 flex items-start gap-2 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-900 dark:border-red-900 dark:bg-red-950/40 dark:text-red-200'
          }
        >
          {integrity.ok ? (
            <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          ) : (
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          )}
          <div>
            {integrity.ok ? (
              <p>
                <strong>Chain intact.</strong> {integrity.verified_count.toLocaleString()} events verified end-to-end.
              </p>
            ) : (
              <p>
                <strong>Chain broken.</strong>{' '}
                {integrity.broken_at ? <>First divergence at event <code>{integrity.broken_at}</code>.</> : null}{' '}
                {integrity.message}
              </p>
            )}
          </div>
        </div>
      )}

      <div className="mt-4">
        {isLoading ? <Skeleton className="h-64" /> : <AuditLogTable entries={data?.items || []} />}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/audit-log')({ component: AuditLogPage })
