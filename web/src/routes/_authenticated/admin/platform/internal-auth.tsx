import { createFileRoute } from '@tanstack/react-router'

import { AdminGuard } from '@/features/admin/internalAuth/AdminGuard'
import { InternalAuthHealth } from '@/features/admin/internalAuth/InternalAuthHealth'
import { TrustedProxyList } from '@/features/admin/internalAuth/TrustedProxyList'
import { PageHeader } from '@/components/shared/PageHeader'
import { internalAuthMessages as M } from '@/i18n/messages/internalAuth'

function InternalAuthPage() {
  return (
    <AdminGuard>
      <div className="space-y-8">
        <PageHeader title={M.pageTitle} description={M.pageDescription} />
        <InternalAuthHealth />
        <TrustedProxyList />
      </div>
    </AdminGuard>
  )
}

export const Route = createFileRoute('/_authenticated/admin/platform/internal-auth')({
  component: InternalAuthPage,
})
