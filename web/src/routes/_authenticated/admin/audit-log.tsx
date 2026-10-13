import { createFileRoute, redirect } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { toast } from 'sonner'
import { getAuditLog, exportAuditCSV } from '@/api/admin'
import { PageHeader } from '@/components/shared/PageHeader'
import { AuditLogTable } from '@/components/admin/AuditLogTable'
import { Button } from '@/components/ui/shadcn/button'

export function AuditLogPage() {
  const { t } = useTranslation('common')
  const { data, isLoading } = useQuery({ queryKey: ['admin', 'audit-log'], queryFn: () => getAuditLog() })

  const handleExport = async () => {
    try {
      await exportAuditCSV()
      toast.success(t('adm.aud.downloaded'))
    } catch {
      toast.error(t('adm.aud.err_export'))
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('adm.aud.title')}
        description={t('adm.aud.description')}
        actions={
          <Button variant="outline" onClick={handleExport} data-testid="audit-export">
            <Download className="h-4 w-4" /> {t('adm.aud.export')}
          </Button>
        }
      />
      <AuditLogTable entries={data?.events || []} isLoading={isLoading} />
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/audit?tab=log). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/audit-log')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/audit', search: { tab: 'log' }, replace: true })
  },
})
