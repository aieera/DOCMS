import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { OcrConfigPage } from './intelligence/ocr-config'
import { OcrReviewPage } from './intelligence/ocr-review'
import { PageHeader } from '@/components/shared/PageHeader'

// Merge #5 — OCR quality config + review queue. Standard config-and-queue
// pattern: one tab for the threshold/profile knobs, one for the docs
// that fell below them. Tab state via ?tab=.
type Tab = 'config' | 'review'
interface S { tab?: Tab }

function OcrPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'config'
  return (
    <div className="space-y-4">
      <PageHeader title="OCR" description="Quality thresholds and the review queue for low-confidence scans." />
      <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/ocr', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="config">Quality config</TabsTrigger>
        <TabsTrigger value="review">Review queue</TabsTrigger>
      </TabsList>
      <TabsContent value="config" className="mt-4"><OcrConfigPage /></TabsContent>
      <TabsContent value="review" className="mt-4"><OcrReviewPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/ocr')({
  component: OcrPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return t === 'config' || t === 'review' ? { tab: t } : {}
  },
})
