import { createFileRoute } from '@tanstack/react-router'

import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { SchedulesList } from '@/features/admin/schedules/SchedulesList'
import { PageHeader } from '@/components/shared/PageHeader'

function SchedulesPage() {
  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader
          title="Temporal Schedules"
          description="Per-tenant Temporal schedules registered by the workflow service. Covers password expiry, acknowledgement reminders, and (Wave 15.4) signature-profile orphan sweeps."
        />
        <SchedulesList />
      </div>
    </AdminGuard>
  )
}

export const Route = createFileRoute('/_authenticated/admin/platform/schedules')({
  component: SchedulesPage,
})
