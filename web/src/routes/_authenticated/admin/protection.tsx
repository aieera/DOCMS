import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { ClassificationPage } from './tenant/classification'
import { WatermarkPage } from './tenant/watermark'
import { IrmPage } from './tenant/irm'

// Merge — Information protection. Classification drives the other two:
// the viewer watermark is applied per-classification, and protected
// exports (IRM) enforce the classification on egress. One content-
// protection surface. Tab state is URL-driven via `?tab=`; the standalone
// /admin/tenant/{classification,watermark,irm} routes still resolve.
type Tab = 'classification' | 'watermark' | 'exports'

const TABS: readonly Tab[] = ['classification', 'watermark', 'exports']

function ProtectionPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'classification'
  return (
    <div className="space-y-4">
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/protection', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="classification">Classification &amp; access</TabsTrigger>
          <TabsTrigger value="watermark">Watermark</TabsTrigger>
          <TabsTrigger value="exports">Protected exports</TabsTrigger>
        </TabsList>
        <TabsContent value="classification" className="mt-4"><ClassificationPage /></TabsContent>
        <TabsContent value="watermark" className="mt-4"><WatermarkPage /></TabsContent>
        <TabsContent value="exports" className="mt-4"><IrmPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/protection')({
  component: ProtectionPage,
  validateSearch: (raw: Record<string, unknown>): { tab?: Tab } =>
    TABS.includes(raw.tab as Tab) ? { tab: raw.tab as Tab } : {},
})
