import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Boxes, ArrowRight } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'

// Top-level Integrations surface — a peer of Workspaces. Each connected system
// gets its own customer-scoped explorer, separate from the tenant's workspaces.
// Built as a list so further integrations slot in beside ERP without a redesign.
interface IntegrationDef {
  key: string
  // Catalogue keys, not copy: this list is a module constant evaluated
  // once at import, so a translated literal would freeze whichever
  // locale happened to load first.
  nameKey: string
  descKey: string
  to?: string
  status: 'active' | 'coming_soon'
}

const INTEGRATIONS: IntegrationDef[] = [
  {
    key: 'erp',
    nameKey: 'integrations.erp_name',
    descKey: 'integrations.erp_desc',
    to: '/integrations/erp',
    status: 'active',
  },
]

function StatusPill({ status }: { status: IntegrationDef['status'] }) {
  const { t } = useTranslation('common')
  const active = status === 'active'
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center rounded-full px-2 py-0.5 text-[11px] font-medium',
        active ? 'bg-success/15 text-foreground' : 'bg-muted text-muted-foreground',
      )}
    >
      {active ? t('integrations.status_active') : t('integrations.status_coming_soon')}
    </span>
  )
}

function IntegrationCard({ it }: { it: IntegrationDef }) {
  const { t } = useTranslation('common')
  return (
    <Card className="flex h-full flex-col gap-3 p-5 transition-colors hover:border-primary/40">
      <div className="flex items-start gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <Boxes className="h-5 w-5" />
        </span>
        <div className="min-w-0">
          <div className="truncate font-semibold text-foreground">{t(it.nameKey)}</div>
          <StatusPill status={it.status} />
        </div>
      </div>
      <p className="text-sm text-muted-foreground">{t(it.descKey)}</p>
      {it.to && (
        <div className="mt-auto flex items-center gap-1 text-sm font-medium text-primary">
          {t('integrations.open')} <ArrowRight className="h-4 w-4 rtl:rotate-180" />
        </div>
      )}
    </Card>
  )
}

function IntegrationsPage() {
  const { t } = useTranslation('common')
  return (
    <div>
      <PageHeader
        title={t('integrations.title')}
        description={t('integrations.description')}
      />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {INTEGRATIONS.map((it) =>
          it.to ? (
            <Link key={it.key} to={it.to} className="block focus-visible:outline-none">
              <IntegrationCard it={it} />
            </Link>
          ) : (
            <div key={it.key} className="cursor-default opacity-70">
              <IntegrationCard it={it} />
            </div>
          ),
        )}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/integrations/')({
  component: IntegrationsPage,
})
