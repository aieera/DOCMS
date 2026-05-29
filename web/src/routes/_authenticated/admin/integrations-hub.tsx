import { createFileRoute, redirect } from '@tanstack/react-router'

// /admin/integrations-hub — legacy URL kept alive only as a redirect
// to the canonical /admin/integrations page. The hub used to host the
// six-tab surface; that content now lives on the canonical URL with
// the same `?tab=` shape so deep-links pasted around the org still
// resolve to the right tab.
type Tab = 'esign' | 'connectors' | 'webhooks' | 'email' | 'events' | 'mcp'
const TABS: readonly Tab[] = ['esign', 'connectors', 'webhooks', 'email', 'events', 'mcp']
interface S { tab?: Tab }

export const Route = createFileRoute('/_authenticated/admin/integrations-hub')({
  validateSearch: (raw: Record<string, unknown>): S => {
    const t = raw.tab
    return TABS.includes(t as Tab) ? { tab: t as Tab } : {}
  },
  beforeLoad: ({ search }) => {
    throw redirect({
      to: '/admin/integrations',
      search: search.tab ? { tab: search.tab } : {},
      replace: true,
    })
  },
})
