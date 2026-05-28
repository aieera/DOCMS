import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { CompliancePage } from './compliance'
import { ResidencyPage } from './residency'

// Consolidated admin surface — Compliance + Residency merged per the
// 2026-05-29 admin-consolidation plan. They already showed overlapping
// region/encryption data; tabs let admins move between the snapshot
// view and the per-document migration workflow without two sidebar
// entries.
//
// Tab state is URL-driven (`?tab=compliance|residency`) so deep links
// from the sidebar / docs / audit log work. The two legacy routes
// (/admin/compliance, /admin/residency) still resolve and render the
// same components for bookmark compatibility.

type Tab = 'compliance' | 'residency'

interface DGSearch {
  tab?: Tab
}

function DataGovernancePage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'compliance'

  return (
    <div className="space-y-4">
      <Tabs
        value={active}
        onValueChange={(v) =>
          navigate({ to: '/admin/data-governance', search: { tab: v as Tab } })
        }
      >
        <TabsList>
          <TabsTrigger value="compliance">Compliance</TabsTrigger>
          <TabsTrigger value="residency">Residency</TabsTrigger>
        </TabsList>
        <TabsContent value="compliance" className="mt-4">
          <CompliancePage />
        </TabsContent>
        <TabsContent value="residency" className="mt-4">
          <ResidencyPage />
        </TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/data-governance')({
  component: DataGovernancePage,
  validateSearch: (raw: Record<string, unknown>): DGSearch => {
    const t = raw.tab
    if (t === 'compliance' || t === 'residency') return { tab: t }
    return {}
  },
})
