import { createFileRoute, Link } from '@tanstack/react-router'
import { Activity, AlertTriangle, Brain, CreditCard, Database, FileJson, FileSearch, FolderSync, FolderTree, KeyRound, LayoutTemplate, Link2, Lock, MapPinned, Plug, Scale, ScrollText, Settings, Shield, ShieldAlert, ShieldCheck, Tags as TagsIcon, Upload, UserCog, Users, Workflow, type LucideIcon } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { useAuthStore } from '@/store/authStore'
import { adminPathAllowsComplianceOfficer } from '@/routes/_authenticated'
import { cn } from '@/lib/cn'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

interface Section {
  to: string
  icon: LucideIcon
  label: string
  desc: string
}

interface SubGroup {
  label: string
  sections: Section[]
}

interface SectionGroup {
  label: string
  description: string
  sections?: Section[]
  subgroups?: SubGroup[]
}

// 2026-07-30 admin-consolidation-2:
// Tenant administration was a flat 24-tile wall. It is now 6 merged tabbed
// pages + 4 named sub-groups. Each merge composes already-adjacent pages as
// `?tab=`-driven tabs (see the merged route files); the old standalone URLs
// (/admin/billing, /admin/siem, /admin/tenant/watermark, …) still resolve and
// render their component for bookmark compatibility — only the navigation
// grouping and canonical entry points changed.
//
// Merges: Identity&Access(+SCIM) · Subscription&licensing(billing+license) ·
// Information protection(classification+watermark+IRM) · Records&retention ·
// Legal holds&e-discovery · Audit&SIEM.
const TENANT_GROUP: SectionGroup = {
  label: 'Tenant administration',
  description: 'People, policy, and tenant-wide configuration.',
  subgroups: [
    {
      label: 'People & access',
      sections: [
        { to: '/admin/identity', icon: Users, label: 'Identity & Access', desc: 'Users, groups, roles, SSO, LDAP/AD, SCIM provisioning' },
        { to: '/admin/api-keys', icon: KeyRound, label: 'API keys', desc: 'Programmatic access' },
        { to: '/admin/share-links', icon: Link2, label: 'Share links', desc: 'Active tenant-wide links' },
      ],
    },
    {
      label: 'Security & protection',
      sections: [
        { to: '/admin/protection', icon: ShieldCheck, label: 'Information protection', desc: 'Classification & access · watermark · protected exports (IRM)' },
        { to: '/admin/tenant/encryption', icon: Lock, label: 'Encryption & keys', desc: 'External KMS (Vault/AWS/Azure) · rotation · break-glass revoke' },
        { to: '/admin/tenant/sync', icon: FolderSync, label: 'Devices & Sync', desc: 'Selective-sync devices, folders, status, revoke' },
      ],
    },
    {
      label: 'Compliance & records',
      sections: [
        { to: '/admin/records-retention', icon: FolderTree, label: 'Records & retention', desc: 'File plan · retention policies & schedules · disposition' },
        { to: '/admin/legal', icon: Scale, label: 'Legal holds & e-discovery', desc: 'Active holds + hold-scoped export (docs + metadata + audit)' },
        { to: '/admin/privacy', icon: UserCog, label: 'Privacy requests', desc: 'GDPR export / erase / anonymize' },
        { to: '/admin/audit', icon: ScrollText, label: 'Audit & SIEM', desc: 'Activity history + forwarding to syslog / Splunk / Sentinel' },
        { to: '/admin/data-governance', icon: ShieldAlert, label: 'Data governance', desc: 'Compliance overview + residency migrations' },
      ],
    },
    {
      label: 'Configuration & operations',
      sections: [
        { to: '/admin/subscription', icon: CreditCard, label: 'Subscription & licensing', desc: 'Plan & usage · license (JWT claims, seats, entitlements, expiry)' },
        { to: '/admin/tenant-settings', icon: Settings, label: 'Tenant settings', desc: 'Feature flags + upload policy' },
        { to: '/admin/metadata-schema', icon: FileJson, label: 'Metadata schema', desc: 'Visual field builder + raw JSON Schema' },
        { to: '/admin/workflows', icon: Workflow, label: 'Workflows', desc: 'Approval workflows' },
        // /templates has no sidebar entry (removed 2026-07-28 by request)
        // and nothing else linked to it, so the page was reachable only
        // by typing the URL. Provisioning a workspace from a template is
        // a setup task, so the admin hub is where it belongs.
        { to: '/templates', icon: LayoutTemplate, label: 'Workspace templates', desc: 'Reusable folder structures — create a workspace from a template' },
        { to: '/admin/bulk', icon: Upload, label: 'Bulk import / export', desc: 'NDJSON migration of workspaces, folders, documents' },
        { to: '/admin/permission-lag', icon: Activity, label: 'Permission propagation', desc: 'Search-index lag p50/p95/p99 vs. 5s SLI' },
      ],
    },
  ],
}

// Consolidated to a single page at /admin/integrations. Old URLs
// (/admin/integrations-hub, /admin/integrations/{email,events,mcp},
// and /admin/connectors / /admin/webhooks as standalone) still
// resolve so external links keep working — they redirect into the
// matching tab on the canonical page.
const INTEGRATIONS_GROUP: SectionGroup = {
  label: 'Integrations',
  description: 'Connect SeDoc to third-party systems — e-signature, M365/Google, email, webhooks, and LLM tool access.',
  sections: [
    { to: '/admin/integrations', icon: Plug, label: 'Integrations', desc: 'eSignature · Connectors · Webhooks · Email · Event streaming · MCP' },
  ],
}

// Intelligence surfaces — 4 consolidated tiles + 3 standalone-kept
// (Routing rules, Filing analytics, Anomaly reports). The merged
// /admin/intelligence/* URLs still resolve for back-compat.
const INTEL_GROUP: SectionGroup = {
  label: 'Intelligence',
  description: 'AI provider config, tagging, OCR, PII/PHI scanning, smart routing, and analytics.',
  sections: [
    { to: '/admin/ai', icon: Brain, label: 'AI & Models', desc: 'Provider keys · NER tier · model registry · usage & cost' },
    { to: '/admin/tagging', icon: TagsIcon, label: 'Tagging', desc: 'Catalog · auto-tag thresholds · review queue' },
    { to: '/admin/ocr', icon: FileSearch, label: 'OCR', desc: 'Quality config + review queue' },
    { to: '/admin/ingestion', icon: Upload, label: 'Ingestion', desc: 'Pre-commit pipeline — review queue + staged items' },
    { to: '/admin/pii-scanning', icon: Shield, label: 'PII / PHI scanning', desc: 'Findings + detection rules' },
    { to: '/admin/intelligence/routing-rules', icon: MapPinned, label: 'Routing rules', desc: 'Smart-routing rules + config' },
    { to: '/admin/intelligence/filing-analytics', icon: FileSearch, label: 'Filing analytics', desc: 'Suggestion acceptance + filing patterns' },
    { to: '/admin/intelligence/anomalies', icon: AlertTriangle, label: 'Anomaly reports', desc: 'Workspace outlier scans' },
  ],
}

// ADR 0069 — platform-admin-only sections. Membership is in the
// `platform_admins` table, separate from a user's tenant role.
const PLATFORM_GROUP: SectionGroup = {
  label: 'Platform administration',
  description: 'Cross-tenant tools — every action is audited.',
  sections: [
    { to: '/admin/platform/support-search', icon: ShieldAlert, label: 'Support search', desc: 'Cross-tenant document search — every query audited' },
    { to: '/admin/platform/db-info', icon: Database, label: 'Database driver info', desc: 'Active driver + version + capability matrix' },
    { to: '/admin/platform/load-tests', icon: Activity, label: 'Load test history', desc: 'Sign-off runs — index built from docs/load-tests at build time' },
  ],
}

function AdminPage() {
  const user = useAuthStore((s) => s.user)
  const isPlatformAdmin = user?.is_platform_admin === true
  const isComplianceOfficer = user?.role === 'compliance_officer' && !isPlatformAdmin

  let groups = [TENANT_GROUP, INTEGRATIONS_GROUP, INTEL_GROUP, ...(isPlatformAdmin ? [PLATFORM_GROUP] : [])]

  // A compliance officer only reaches the hub for the intelligence
  // pages the backend authorizes them to read (see the route guard's
  // COMPLIANCE_OFFICER_ADMIN_PATHS) — show exactly those cards so the
  // hub never advertises a destination that would bounce them.
  if (isComplianceOfficer) {
    groups = [
      {
        ...INTEL_GROUP,
        sections: (INTEL_GROUP.sections ?? []).filter((sec) =>
          adminPathAllowsComplianceOfficer(sec.to),
        ),
      },
    ]
  }

  return (
    <div className="space-y-10">
      <PageHeader
        title="Administration"
        description={
          isComplianceOfficer
            ? 'Intelligence oversight — read-only access to scanning, tagging, and anomaly surfaces.'
            : 'Manage tenant-wide policy, people, and intelligence settings.'
        }
      />
      {groups.map((g) => (
        <SectionGroupBlock key={g.label} group={g} />
      ))}
    </div>
  )
}

function SectionGroupBlock({ group }: { group: SectionGroup }) {
  return (
    <section aria-labelledby={`group-${slugify(group.label)}`}>
      <div className="mb-4 border-b border-border pb-2">
        <h2
          id={`group-${slugify(group.label)}`}
          className="text-xs font-semibold uppercase tracking-wider text-muted-foreground"
        >
          {group.label}
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">{group.description}</p>
      </div>
      {group.subgroups ? (
        <div className="space-y-6">
          {group.subgroups.map((sg) => (
            <div key={sg.label}>
              <h3 className="mb-3 text-sm font-medium text-foreground">{sg.label}</h3>
              <SectionGrid sections={sg.sections} />
            </div>
          ))}
        </div>
      ) : (
        <SectionGrid sections={group.sections ?? []} />
      )}
    </section>
  )
}

function SectionGrid({ sections }: { sections: Section[] }) {
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      {sections.map((s) => (
        <SectionCard key={s.to} section={s} />
      ))}
    </div>
  )
}

function SectionCard({ section: { to, icon: Icon, label, desc } }: { section: Section }) {
  return (
    <Link
      to={to}
      className={cn(
        'block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background rounded-lg',
      )}
    >
      {/* Card hover: a subtle primary-tinted border + slight bg lift,
          matching the workspace cards' pattern so the admin grid feels
          part of the same product. Icon sits in a fixed 40px tile with
          a primary-tinted background that intensifies on hover. */}
      <Card className="group flex h-full items-start gap-3 border border-transparent p-4 transition-all hover:-translate-y-0.5 hover:border-primary/40 hover:bg-accent/50">
        <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary transition-colors group-hover:bg-primary group-hover:text-primary-foreground">
          <Icon className="h-6 w-6" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="flex items-center justify-between gap-1 truncate text-sm font-medium">
            <span className="truncate">{label}</span>
            <DirectionalIcon name="ChevronRight" className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-foreground" />
          </p>
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{desc}</p>
        </div>
      </Card>
    </Link>
  )
}

function slugify(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export const Route = createFileRoute('/_authenticated/admin/')({ component: AdminPage })
