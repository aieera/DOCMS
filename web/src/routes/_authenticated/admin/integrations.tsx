// /admin/integrations — layout-only parent route.
//
// The child routes are:
//   ./index.tsx  — connections + envelopes + notifications tabs
//   ./events.tsx — event-streaming console (ADR 0077)
//   ./email.tsx  — email ingestion configs (ADR 0087)
//
// Before this split, integrations.tsx itself rendered the tabbed
// content with no <Outlet />, so navigating to /events or /email
// re-rendered the parent without ever showing the child. The fix is
// the standard TanStack pattern: layout file is a thin <Outlet />,
// each child page is its own file.
import { createFileRoute, Outlet } from '@tanstack/react-router'

export const Route = createFileRoute('/_authenticated/admin/integrations')({
  component: IntegrationsLayout,
})

function IntegrationsLayout() {
  return <Outlet />
}
