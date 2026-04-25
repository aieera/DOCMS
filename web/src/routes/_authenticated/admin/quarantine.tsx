import { createFileRoute } from '@tanstack/react-router'

import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { QuarantineQueue } from '@/features/admin/quarantine/QuarantineQueue'
import { PageHeader } from '@/components/shared/PageHeader'

function QuarantinePage() {
  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader
          title="Quarantine review"
          description="Files flagged by the virus scanner or MIME deny-list. Release moves a file back to hot storage; delete removes it permanently. Every action is audited."
        />
        <QuarantineQueue />
      </div>
    </AdminGuard>
  )
}

export const Route = createFileRoute('/_authenticated/admin/quarantine')({
  component: QuarantinePage,
})
