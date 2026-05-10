import { createFileRoute } from '@tanstack/react-router'
import { CreditCard } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { EmptyState } from '@/components/ui/EmptyState'

function BillingPage() {
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

export const Route = createFileRoute('/_authenticated/admin/billing')({ component: BillingPage })
