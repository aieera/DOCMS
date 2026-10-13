import { createFileRoute, redirect } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { CreditCard } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'

export function BillingPage() {
  const { t } = useTranslation('common')
  return (
    <div className="space-y-6">
      <PageHeader title={t('adm.bill.title')} description={t('adm.bill.description')} />
      <EmptyState
        icon={<CreditCard className="h-6 w-6" />}
        title={t('adm.bill.empty')}
        description={t('adm.bill.empty_desc')}
      />
    </div>
  )
}

// Merged surface — this standalone URL redirects into the canonical
// tabbed page (/admin/subscription?tab=plan). The page component stays
// exported so the shell can embed it: one rendering, one URL.
export const Route = createFileRoute('/_authenticated/admin/billing')({
  beforeLoad: () => {
    throw redirect({ to: '/admin/subscription', search: { tab: 'plan' }, replace: true })
  },
})
