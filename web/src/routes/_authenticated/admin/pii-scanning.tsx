import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { ComplianceConfigPage } from './intelligence/compliance-config'
import { ComplianceAdminDashboard } from './intelligence/compliance'
import { PageHeader } from '@/components/shared/PageHeader'

// Merge #6 — PII/PHI scanning. Config (rules + thresholds) on one
// tab, findings dashboard on the other.
type Tab = 'config' | 'findings'
interface S { tab?: Tab }

function PIIScanningPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'findings'
  return (
    <div className="space-y-4">
      <PageHeader title="PII / PHI scanning" description="Findings across the tenant and the detection rules that produce them." />
      <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/pii-scanning', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="findings">Findings</TabsTrigger>
        <TabsTrigger value="config">Detection rules</TabsTrigger>
      </TabsList>
      <TabsContent value="findings" className="mt-4"><ComplianceAdminDashboard /></TabsContent>
      <TabsContent value="config" className="mt-4"><ComplianceConfigPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/pii-scanning')({
  component: PIIScanningPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return t === 'config' || t === 'findings' ? { tab: t } : {}
  },
})
