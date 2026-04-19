import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import toast from 'react-hot-toast'
import { getAuditLog, exportAuditCSV } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { AuditLogTable } from '@/components/admin/AuditLogTable'
import { Button } from '@/components/ui/Button'
import { Skeleton } from '@/components/ui/Skeleton'

function AuditLogPage() {
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'audit-log'], queryFn: () => getAuditLog() })

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
        description="Activity history across the tenant"
        actions={
          <Button variant="ghost" onClick={handleExport} data-testid="audit-export">
            <Download className="mr-1 h-4 w-4" /> Export CSV
          </Button>
        }
      />
      {isLoading ? <Skeleton className="h-64" /> : <AuditLogTable entries={data?.items || []} />}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/audit-log')({ component: AuditLogPage })
