import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/shadcn/tabs'
import { IntegrationsIndexPage } from './integrations/index'
import { ConnectorsPage } from './connectors'
import { WebhooksPage } from './webhooks'
import { EmailIngestionPage } from './integrations/email'
import { EventStreamPage } from './integrations/events'
import { MCPPage } from './integrations/mcp'

// Merge #8 — Integrations hub. Combines eSignature + Connectors +
// Webhooks + Event streaming + Email ingestion + MCP into a single
// tabbed surface.
type Tab = 'esign' | 'connectors' | 'webhooks' | 'email' | 'events' | 'mcp'
interface S { tab?: Tab }
const TABS: readonly Tab[] = ['esign', 'connectors', 'webhooks', 'email', 'events', 'mcp'] as const

function IntegrationsHubPage() {
  const navigate = useNavigate()
  const { tab } = Route.useSearch()
  const active: Tab = tab ?? 'esign'
  return (
    <Tabs
      value={active}
      onValueChange={(v) => navigate({ to: '/admin/integrations-hub', search: { tab: v as Tab } })}
    >
      <TabsList>
        <TabsTrigger value="esign">eSignature</TabsTrigger>
        <TabsTrigger value="connectors">Connectors</TabsTrigger>
        <TabsTrigger value="webhooks">Webhooks</TabsTrigger>
        <TabsTrigger value="email">Email ingestion</TabsTrigger>
        <TabsTrigger value="events">Event streaming</TabsTrigger>
        <TabsTrigger value="mcp">MCP</TabsTrigger>
      </TabsList>
      <TabsContent value="esign" className="mt-4"><IntegrationsIndexPage /></TabsContent>
      <TabsContent value="connectors" className="mt-4"><ConnectorsPage /></TabsContent>
      <TabsContent value="webhooks" className="mt-4"><WebhooksPage /></TabsContent>
      <TabsContent value="email" className="mt-4"><EmailIngestionPage /></TabsContent>
      <TabsContent value="events" className="mt-4"><EventStreamPage /></TabsContent>
      <TabsContent value="mcp" className="mt-4"><MCPPage /></TabsContent>
    </Tabs>
  )
}

export const Route = createFileRoute('/_authenticated/admin/integrations-hub')({
  component: IntegrationsHubPage,
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return TABS.includes(t as Tab) ? { tab: t as Tab } : {}
  },
})
