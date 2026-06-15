import { Link, useRouterState } from '@tanstack/react-router'
import { Home } from 'lucide-react'
import { Fragment, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import type { Workspace } from '@/types/api'

// Maps URL segments to a human label. Anything not in the map gets a
// title-cased version of the segment as a fallback ("audit-log" →
// "Audit log"). Add entries here as new sections ship; keeping this
// table tiny is intentional — it's just for navigation crumbs, not a
// full route registry.
const SEGMENT_LABELS: Record<string, string> = {
  '': 'Home',
  workspaces: 'Workspaces',
  documents: 'Document',
  search: 'Search',
  ask: 'Ask',
  trash: 'Trash',
  tasks: 'Tasks',
  notifications: 'Notifications',
  'saved-searches': 'Saved searches',
  settings: 'Settings',
  security: 'Security',
  mfa: 'MFA',
  recovery: 'Recovery',
  admin: 'Admin',
  users: 'Users',
  groups: 'Groups',
  permissions: 'Permissions',
  'permission-lag': 'Permission lag',
  'api-keys': 'API keys',
  'audit-log': 'Audit log',
  billing: 'Billing',
  compliance: 'Compliance',
  connectors: 'Connectors',
  integrations: 'Integrations',
  'legal-holds': 'Legal holds',
  'metadata-schema': 'Metadata schema',
  privacy: 'Privacy',
  residency: 'Residency',
  retention: 'Retention',
  'share-links': 'Share links',
  sso: 'SSO',
  tags: 'Tags',
  webhooks: 'Webhooks',
  workflows: 'Workflows',
  designer: 'Designer',
  instances: 'Instances',
  intelligence: 'Intelligence',
  anomalies: 'Anomalies',
  'auto-tag': 'Auto-tag',
  'compliance-config': 'Compliance config',
  'filing-analytics': 'Filing analytics',
  models: 'Models',
  'ner-config': 'NER config',
  'ocr-review': 'OCR review',
  'ocr-config': 'OCR quality config',
  'routing-rules': 'Routing rules',
  'tag-review': 'Tag review',
  usage: 'Usage',
  platform: 'Platform',
  'support-search': 'Support search',
  'db-info': 'Db-info',
  'load-tests': 'Load tests',
  ipaas: 'IPaaS',
  tenant: 'Tenant',
  ai: 'AI',
  identity: 'Identity',
  ldap: 'LDAP',
  sign: 'Sign',
  signatures: 'Signatures',
  send: 'Send',
}

function humanize(segment: string): string {
  if (SEGMENT_LABELS[segment]) return SEGMENT_LABELS[segment]
  // Looks like a UUID or hex id → keep short ellipsis form so the
  // crumb stays readable. Stops the breadcrumb from showing
  // "33333333-3333-7333-…" as a label.
  if (/^[0-9a-f-]{8,}$/i.test(segment)) return segment.slice(0, 8) + '…'
  return segment
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

// UUID_RE matches v4/v7-style UUIDs as they appear in route params.
const UUID_RE = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i

// findIdAfter — return segments[i+1] when segments[i] === marker AND
// that following segment is a UUID. Lets us subscribe to a specific
// entity's React Query cache key for the visible URL.
function findIdAfter(segments: string[], marker: string): string | undefined {
  const i = segments.indexOf(marker)
  if (i < 0) return undefined
  const next = segments[i + 1]
  if (!next || !UUID_RE.test(next)) return undefined
  return next
}

export function Breadcrumbs() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const segments = useMemo(() => pathname.split('/').filter(Boolean), [pathname])

  // Extract the IDs the URL surfaces. Subscribing via useQuery with
  // `enabled: false` gives us a reactive read of whatever the
  // detail-page consumers / sidebar list have already populated;
  // we never fire a fetch from the crumb itself. When the URL has
  // no id of a given kind, we still call useQuery (hook-order rule)
  // with a sentinel key so the hook doesn't read garbage.
  const workspaceId = findIdAfter(segments, 'workspaces')
  const documentId = findIdAfter(segments, 'documents')
  const workflowId = findIdAfter(segments, 'instances')

  // Subscribe-only: `enabled: false` keeps the breadcrumb from
  // firing its own fetch — it reads whatever the page-level
  // consumers have already cached. v5 still requires `queryFn` to
  // be declared even on disabled queries, hence the stub.
  const noopQuery = () => Promise.resolve(undefined as never)
  const workspace = useQuery<{ name?: string }>({
    queryKey: ['workspace', workspaceId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })
  const workspacesList = useQuery<Workspace[]>({
    queryKey: ['workspaces'],
    queryFn: noopQuery,
    enabled: false,
  })
  const documentQ = useQuery<{ title?: string }>({
    queryKey: ['document', documentId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })
  const workflow = useQuery<{ definition_name?: string }>({
    queryKey: ['workflow-instance', workflowId ?? '__none__'],
    queryFn: noopQuery,
    enabled: false,
  })

  // Workspace name lookup: prefer the detail-page cache, then fall
  // back to scanning the sidebar's ['workspaces'] list. The list is
  // hydrated on every authenticated page via WorkspaceSelector +
  // the dashboard KPI row, so it's nearly always available even
  // when the workspace detail page was never visited.
  const workspaceName =
    workspace.data?.name ??
    workspacesList.data?.find((w) => w.id === workspaceId)?.name

  const crumbs = useMemo(() => {
    let href = ''
    return segments.map((segment) => {
      href += '/' + segment
      let label: string | undefined
      if (segment === workspaceId) label = workspaceName
      else if (segment === documentId) label = documentQ.data?.title
      else if (segment === workflowId) label = workflow.data?.definition_name
      return { segment, href, label: label ?? humanize(segment) }
    })
  }, [
    segments,
    workspaceId,
    workspaceName,
    documentId,
    documentQ.data?.title,
    workflowId,
    workflow.data?.definition_name,
  ])

  return (
    <nav aria-label="Breadcrumb" className="flex min-w-0 items-center text-sm">
      <Link
        to="/"
        className="flex shrink-0 items-center gap-1 text-sidebar-foreground/60 transition-colors hover:text-white"
      >
        <Home className="h-3.5 w-3.5" />
        <span className="sr-only">Home</span>
      </Link>
      {crumbs.map((crumb, idx) => {
        const isLast = idx === crumbs.length - 1
        return (
          <Fragment key={crumb.href}>
            <DirectionalIcon name="ChevronRight" className="mx-1 h-3.5 w-3.5 shrink-0 text-sidebar-foreground/40" aria-hidden />
            {isLast ? (
              <span
                className="block max-w-[28ch] truncate font-semibold text-white sm:max-w-[44ch]"
                title={crumb.label}
              >
                {crumb.label}
              </span>
            ) : (
              <Link
                to={crumb.href}
                className="block max-w-[20ch] truncate text-sidebar-foreground/60 transition-colors hover:text-white"
                title={crumb.label}
              >
                {crumb.label}
              </Link>
            )}
          </Fragment>
        )
      })}
    </nav>
  )
}
