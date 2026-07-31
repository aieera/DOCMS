import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { LegalHoldsPage } from './legal-holds'
import { EDiscoveryPage } from './ediscovery'

// Merge — Legal holds & e-discovery. E-discovery is hold-scoped export
// (docs + metadata + audit for a hold), so it operates directly on the
// holds managed in the first tab. Tab state is URL-driven via `?tab=`;
// the standalone /admin/legal-holds and /admin/ediscovery routes still
// resolve for back-compat.
type Tab = 'holds' | 'ediscovery'

function LegalPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'holds'
  return (
    <div className="space-y-4">
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/legal', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="holds">Legal holds</TabsTrigger>
          <TabsTrigger value="ediscovery">E-discovery export</TabsTrigger>
        </TabsList>
        <TabsContent value="holds" className="mt-4"><LegalHoldsPage /></TabsContent>
        <TabsContent value="ediscovery" className="mt-4"><EDiscoveryPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/legal')({
  component: LegalPage,
  validateSearch: (raw: Record<string, unknown>): { tab?: Tab } =>
    raw.tab === 'holds' || raw.tab === 'ediscovery' ? { tab: raw.tab } : {},
})
