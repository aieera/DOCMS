import { useTranslation } from 'react-i18next'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { TenantAIPage } from './tenant/ai'
import { NERConfigPage } from './intelligence/ner-config'
import { ModelsPage } from './intelligence/models'
import { LLMUsagePage } from './intelligence/usage'
import { PageHeader } from '@/components/shared/PageHeader'

// Merge #2 — AI provider + NER tier + Model registry + LLM usage.
// Per the consolidation plan: the duplicate usage tally that used to
// show on both AI provider and LLM usage now lives only on the
// Usage & cost tab. The Provider & keys tab keeps the credential
// management without re-rendering the same usage chart.
type Tab = 'provider' | 'ner' | 'models' | 'usage'
interface S { tab?: Tab }
const TABS: readonly Tab[] = ['provider', 'ner', 'models', 'usage'] as const

function AIPage() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'provider'
  return (
    <div className="space-y-4">
      <PageHeader title={t('adm.aim.title')} description={t('adm.aim.description')} />
      <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/ai', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="provider">{t('adm.aim.tab_provider')}</TabsTrigger>
        <TabsTrigger value="ner">{t('adm.aim.tab_ner')}</TabsTrigger>
        <TabsTrigger value="models">{t('adm.aim.tab_models')}</TabsTrigger>
        <TabsTrigger value="usage">{t('adm.aim.tab_usage')}</TabsTrigger>
      </TabsList>
      <TabsContent value="provider" className="mt-4"><TenantAIPage /></TabsContent>
      <TabsContent value="ner" className="mt-4"><NERConfigPage /></TabsContent>
      <TabsContent value="models" className="mt-4"><ModelsPage /></TabsContent>
      <TabsContent value="usage" className="mt-4"><LLMUsagePage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/ai')({
  component: AIPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return TABS.includes(t as Tab) ? { tab: t as Tab } : {}
  },
})
