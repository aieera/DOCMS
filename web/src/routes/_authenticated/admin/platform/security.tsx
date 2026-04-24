import { createFileRoute } from '@tanstack/react-router'

import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { SecurityPostureCards } from '@/features/admin/security/SecurityPostureCards'
import { PageHeader } from '@/components/shared/PageHeader'

function SecurityPosturePage() {
  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader
          title="Security posture"
          description="Latest CI security-gate outcomes (ADR 0033). Read-only — fixes live in the corresponding workflow run, not here."
        />
        <SecurityPostureCards />
      </div>
    </AdminGuard>
  )
}

export const Route = createFileRoute('/_authenticated/admin/platform/security')({
  component: SecurityPosturePage,
})
