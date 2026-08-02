import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { TagsPage } from './tags'
import { AutoTagAdminPage } from './intelligence/auto-tag'
import { TagReviewQueuePage } from './intelligence/tag-review'
import { PageHeader } from '@/components/shared/PageHeader'

// Merge #4 — Tags catalog + Auto-tag config + Tag review queue.
type Tab = 'catalog' | 'thresholds' | 'review'
interface S { tab?: Tab }

function TaggingPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'catalog'
  return (
    <div className="space-y-4">
      <PageHeader title="Tagging" description="Tag catalog, auto-tag thresholds, and the suggestion review queue." />
      <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/tagging', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="catalog">Catalog</TabsTrigger>
        <TabsTrigger value="thresholds">Thresholds</TabsTrigger>
        <TabsTrigger value="review">Review queue</TabsTrigger>
      </TabsList>
      <TabsContent value="catalog" className="mt-4"><TagsPage /></TabsContent>
      <TabsContent value="thresholds" className="mt-4"><AutoTagAdminPage /></TabsContent>
      <TabsContent value="review" className="mt-4"><TagReviewQueuePage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/tagging')({
  component: TaggingPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return t === 'catalog' || t === 'thresholds' || t === 'review' ? { tab: t } : {}
  },
})
