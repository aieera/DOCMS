import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { toast } from 'sonner'
import { getAuditLog, exportAuditCSV } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { AuditLogTable } from '@/components/admin/AuditLogTable'
import { Button } from '@/components/ui/shadcn/button'

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
    <div className="space-y-6">
      <PageHeader
        title="Audit log"
        description="Every action across the tenant — sign-ins, document edits, permission changes, admin operations. Tenant-isolated and tamper-evident."
        actions={
          <Button variant="outline" onClick={handleExport} data-testid="audit-export">
            <Download className="h-4 w-4" /> Export CSV
          </Button>
        }
      />
      <AuditLogTable entries={data?.events || []} isLoading={isLoading} />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/audit-log')({ component: AuditLogPage })
