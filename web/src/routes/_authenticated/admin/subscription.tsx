import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { BillingPage } from './billing'
import { LicensePage } from './tenant/license'

// Merge — Subscription & licensing. Plan + usage (billing) and the license
// (JWT claims, seats, feature entitlements, expiry) are one entitlement
// surface: the plan is what the license grants. Tab state is URL-driven via
// `?tab=`; the standalone /admin/billing and /admin/tenant/license routes
// still resolve for back-compat.
type Tab = 'plan' | 'license'

function SubscriptionPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'plan'
  return (
    <div className="space-y-4">
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/subscription', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="plan">Plan &amp; usage</TabsTrigger>
          <TabsTrigger value="license">License</TabsTrigger>
        </TabsList>
        <TabsContent value="plan" className="mt-4"><BillingPage /></TabsContent>
        <TabsContent value="license" className="mt-4"><LicensePage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/subscription')({
  component: SubscriptionPage,
  validateSearch: (raw: Record<string, unknown>): { tab?: Tab } =>
    raw.tab === 'plan' || raw.tab === 'license' ? { tab: raw.tab } : {},
})
