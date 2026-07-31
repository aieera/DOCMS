import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { RecordsAdminPage } from './records'
import { RetentionPage } from './retention'

// Merge — Records & retention. The records file plan already owns retention
// schedules + disposition, and the retention page owns retention policies —
// the same records-management domain. Tab state is URL-driven via `?tab=`;
// the standalone /admin/records and /admin/retention routes still resolve
// for back-compat.
type Tab = 'records' | 'retention'

function RecordsRetentionPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'records'
  return (
    <div className="space-y-4">
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/records-retention', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="records">Records file plan</TabsTrigger>
          <TabsTrigger value="retention">Retention policies</TabsTrigger>
        </TabsList>
        <TabsContent value="records" className="mt-4"><RecordsAdminPage /></TabsContent>
        <TabsContent value="retention" className="mt-4"><RetentionPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/records-retention')({
  component: RecordsRetentionPage,
  validateSearch: (raw: Record<string, unknown>): { tab?: Tab } =>
    raw.tab === 'records' || raw.tab === 'retention' ? { tab: raw.tab } : {},
})
