import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'
import { Bell } from 'lucide-react'

function NotificationsPage() {
  return (
    <div>
      <PageHeader title="Notifications" />
      <EmptyState icon={<Bell className="h-12 w-12" />} title="No notifications" description="You're all caught up" />
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/notifications')({ component: NotificationsPage })
