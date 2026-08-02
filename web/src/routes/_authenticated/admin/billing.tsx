import { createFileRoute, redirect } from '@tanstack/react-router'
import { CreditCard } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'

export function BillingPage() {
  return (
    <div className="space-y-6">
      <PageHeader title="Billing" description="Plan, usage, and invoices for this tenant." />
      <EmptyState
        icon={<CreditCard className="h-6 w-6" />}
        title="Billing not yet configured"
        description="Hook up a billing provider in tenant settings to surface usage metrics, invoices, and plan changes here."
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
