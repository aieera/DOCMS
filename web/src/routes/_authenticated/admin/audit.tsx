import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { AuditLogPage } from './audit-log'
import { SIEMPage } from './siem'

// Merge — Audit & SIEM. The activity history (audit log) and the
// forwarding of those same events to syslog / Splunk / Sentinel are one
// surface. Tab state is URL-driven via `?tab=` so deep links keep working;
// the standalone /admin/audit-log and /admin/siem routes still resolve.
type Tab = 'log' | 'forwarding'

function AuditAndSiemPage() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'log'
  return (
    <div className="space-y-4">
      <Tabs value={active} onValueChange={(v) => navigate({ to: '/admin/audit', search: { tab: v as Tab } })}>
        <TabsList>
          <TabsTrigger value="log">{t('adm.aud.tab_log')}</TabsTrigger>
          <TabsTrigger value="forwarding">{t('adm.aud.tab_forwarding')}</TabsTrigger>
        </TabsList>
        <TabsContent value="log" className="mt-4"><AuditLogPage /></TabsContent>
        <TabsContent value="forwarding" className="mt-4"><SIEMPage /></TabsContent>
      </Tabs>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/audit')({
  component: AuditAndSiemPage,
  validateSearch: (raw: Record<string, unknown>): { tab?: Tab } =>
    raw.tab === 'log' || raw.tab === 'forwarding' ? { tab: raw.tab } : {},
})
