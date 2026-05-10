import { Link, useRouterState } from '@tanstack/react-router'
import { ChevronRight, Home } from 'lucide-react'
import { Fragment, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'

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
  'routing-rules': 'Routing rules',
  'tag-review': 'Tag review',
  usage: 'Usage',
  platform: 'Platform',
  'support-search': 'Support search',
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

// isUUIDish matches v4/v7-style UUIDs and the 8-char shortened form
// the legacy fallback emits.
const UUID_RE = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i

// resolveIDLabel reads the React Query cache for a known entity
// label keyed by id. Returns null when there's no cached entity for
// the segment context, in which case the caller falls back to the
// truncated-uuid humanize. Cache keys here mirror what the data
// hooks use (web/src/hooks/useWorkspaces, useDocuments, etc).
function resolveIDLabel(qc: ReturnType<typeof useQueryClient>, segments: string[], idx: number): string | null {
  const segment = segments[idx]
  if (!UUID_RE.test(segment)) return null
  const prevSegment = segments[idx - 1]

  if (prevSegment === 'workspaces') {
    const ws = qc.getQueryData<{ name?: string }>(['workspace', segment])
    if (ws?.name) return ws.name
  }
  if (prevSegment === 'documents') {
    const doc = qc.getQueryData<{ title?: string }>(['document', segment])
    if (doc?.title) return doc.title
  }
  if (prevSegment === 'instances') {
    const wf = qc.getQueryData<{ definition_name?: string }>(['workflow-instance', segment])
    if (wf?.definition_name) return wf.definition_name
  }
  return null
}

export function Breadcrumbs() {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const qc = useQueryClient()

  const crumbs = useMemo(() => {
    const segments = pathname.split('/').filter(Boolean)
    let href = ''
    return segments.map((segment, idx) => {
      href += '/' + segment
      const cached = resolveIDLabel(qc, segments, idx)
      return { segment, href, label: cached ?? humanize(segment) }
    })
  }, [pathname, qc])

  return (
    <nav aria-label="Breadcrumb" className="flex items-center text-sm">
      <Link
        to="/"
        className="flex items-center gap-1 text-muted-foreground transition-colors hover:text-foreground"
      >
        <Home className="h-3.5 w-3.5" />
        <span className="sr-only">Home</span>
      </Link>
      {crumbs.map((crumb, idx) => {
        const isLast = idx === crumbs.length - 1
        return (
          <Fragment key={crumb.href}>
            <ChevronRight className="mx-1 h-3.5 w-3.5 text-muted-foreground/50" aria-hidden />
            {isLast ? (
              <span className="font-medium text-foreground">{crumb.label}</span>
            ) : (
              <Link
                to={crumb.href}
                className="text-muted-foreground transition-colors hover:text-foreground"
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
