// Shared URL-segment → human label table.
//
// Two consumers: the topbar breadcrumb (components/layout/breadcrumbs.tsx)
// and the per-route document title (lib/documentTitle.ts). Keeping one
// table means a page can't be called "Audit log" in the crumb and
// "Audit-log" in the browser tab.
//
// Anything not listed falls back to a title-cased version of the segment
// ("audit-log" → "Audit log"). Add entries only where that fallback reads
// badly — the table is deliberately small.
export const SEGMENT_LABELS: Record<string, string> = {
  '': 'Home',
  workspaces: 'Workspaces',
  documents: 'Document',
  search: 'Search',
  ask: 'Ask',
  trash: 'Trash',
  tasks: 'Tasks',
  notifications: 'Notifications',
  'saved-searches': 'Saved searches',
  'shared-with-me': 'Shared with me',
  clauses: 'Clauses',
  templates: 'Templates',
  reports: 'Reports',
  settings: 'Settings',
  security: 'Security',
  mfa: 'MFA',
  recovery: 'Recovery codes',
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
  'ner-config': 'Entity extraction config',
  'ocr-review': 'Text recognition review',
  'ocr-config': 'Text recognition quality',
  'routing-rules': 'Routing rules',
  'tag-review': 'Tag review',
  usage: 'Usage',
  platform: 'Platform',
  'support-search': 'Support search',
  'db-info': 'Database info',
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

// ID_LIKE matches the opaque path segments (UUIDs, share tokens) that
// must never be shown as a label.
const ID_LIKE = /^[0-9a-f-]{8,}$/i

export function looksLikeId(segment: string): boolean {
  return ID_LIKE.test(segment)
}

export function humanizeSegment(segment: string): string {
  if (SEGMENT_LABELS[segment]) return SEGMENT_LABELS[segment]
  // Looks like a UUID or hex id → keep short ellipsis form so the
  // crumb stays readable. Stops the breadcrumb from showing
  // "33333333-3333-7333-…" as a label.
  if (looksLikeId(segment)) return segment.slice(0, 8) + '…'
  return segment
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}
