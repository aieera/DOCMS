import { createFileRoute, Link } from '@tanstack/react-router'
import { Boxes, ArrowRight } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'

// Top-level Integrations surface — a peer of Workspaces. Each connected system
// gets its own customer-scoped explorer, separate from the tenant's workspaces.
// Built as a list so further integrations slot in beside ERP without a redesign.
interface IntegrationDef {
  key: string
  name: string
  description: string
  to?: string
  status: 'active' | 'coming_soon'
}

const INTEGRATIONS: IntegrationDef[] = [
  {
    key: 'erp',
    name: 'ERP (RAABYT One)',
    description:
      'Customer documents synced one-way from the ERP — quotes, POs, SOs, DOs, invoices and attachments, filed automatically into each customer’s folder tree.',
    to: '/integrations/erp',
    status: 'active',
  },
]

function StatusPill({ status }: { status: IntegrationDef['status'] }) {
  const active = status === 'active'
  return (
    <span
      className={cn(
        'inline-flex w-fit items-center rounded-full px-2 py-0.5 text-[11px] font-medium',
        active ? 'bg-success/15 text-success' : 'bg-muted text-muted-foreground',
      )}
    >
      {active ? 'Active' : 'Coming soon'}
    </span>
  )
}

function IntegrationCard({ it }: { it: IntegrationDef }) {
  return (
    <Card className="flex h-full flex-col gap-3 p-5 transition-colors hover:border-primary/40">
      <div className="flex items-start gap-3">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary">
          <Boxes className="h-5 w-5" />
        </span>
        <div className="min-w-0">
          <div className="truncate font-semibold text-foreground">{it.name}</div>
          <StatusPill status={it.status} />
        </div>
      </div>
      <p className="text-sm text-muted-foreground">{it.description}</p>
      {it.to && (
        <div className="mt-auto flex items-center gap-1 text-sm font-medium text-primary">
          Open <ArrowRight className="h-4 w-4 rtl:rotate-180" />
        </div>
      )}
    </Card>
  )
}

function IntegrationsPage() {
  return (
    <div>
      <PageHeader
        title="Integrations"
        description="Connected systems that file documents into SeDoc. Each integration keeps its own customer-scoped surface, separate from your workspaces."
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
