import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { CreditCard } from 'lucide-react'

function BillingPage() {
  return (
    <div>
      <PageHeader title="Billing" description="Usage and billing overview" />
      <div className="flex flex-col items-center justify-center rounded-lg border border-dashed p-12 text-center">
        <CreditCard className="h-10 w-10 text-muted-foreground" />
        <h3 className="mt-4 text-lg font-medium">No billing information</h3>
        <p className="mt-1 text-sm text-muted-foreground">View your usage metrics and manage billing details.</p>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/billing')({ component: BillingPage })
