import { createFileRoute, Link } from '@tanstack/react-router'
import { Activity, AlertTriangle, Antenna, Archive, Brain, CreditCard, FileJson, FileSearch, Globe, Key, KeyRound, Languages, Link2, Mail, MapPinned, Network, PenTool, Plug, Scale, ScrollText, Settings, Shield, ShieldAlert, ShieldCheck, Sparkles, Tag, Tags as TagsIcon, Upload, UserCog, Users, Webhook, Workflow, Bot, Database, type LucideIcon } from 'lucide-react'
import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { useAuthStore } from '@/store/authStore'
import { cn } from '@/lib/cn'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'

interface Section {
  to: string
  icon: LucideIcon
  label: string
  desc: string
}

interface SectionGroup {
  label: string
  description: string
  sections: Section[]
}

const TENANT_GROUP: SectionGroup = {
  label: 'Tenant administration',
  description: 'People, policy, integrations, and tenant-wide configuration.',
  sections: [
    { to: '/admin/users', icon: Users, label: 'Users', desc: 'Manage team members' },
    { to: '/admin/groups', icon: Shield, label: 'Groups', desc: 'Group permissions' },
    { to: '/admin/permissions', icon: ShieldCheck, label: 'Permission matrix', desc: 'Role × resource grid (read-only)' },
    { to: '/admin/sso', icon: Key, label: 'SSO', desc: 'SAML / OIDC config' },
    { to: '/admin/tenant/identity/ldap', icon: Network, label: 'LDAP / AD', desc: 'Direct-bind + group sync' },
    { to: '/admin/workflows', icon: Workflow, label: 'Workflows', desc: 'Approval workflows' },
    { to: '/admin/retention', icon: Archive, label: 'Retention', desc: 'Retention policies' },
    { to: '/admin/legal-holds', icon: Scale, label: 'Legal holds', desc: 'Active holds' },
    { to: '/admin/audit-log', icon: ScrollText, label: 'Audit log', desc: 'Activity history' },
    { to: '/admin/webhooks', icon: Webhook, label: 'Webhooks', desc: 'Event subscriptions' },
    { to: '/admin/integrations/events', icon: Antenna, label: 'Event streaming', desc: 'Per-tenant event stream' },
    { to: '/admin/integrations/email', icon: Mail, label: 'Email ingestion', desc: 'Microsoft / Gmail / IMAP → documents' },
    { to: '/admin/tags', icon: Tag, label: 'Tags', desc: 'Tenant tag catalog' },
    { to: '/admin/metadata-schema', icon: FileJson, label: 'Metadata schema', desc: 'Custom-field JSON Schema' },
    { to: '/admin/share-links', icon: Link2, label: 'Share links', desc: 'Active tenant-wide links' },
    { to: '/admin/api-keys', icon: KeyRound, label: 'API keys', desc: 'Programmatic access' },
    { to: '/admin/connectors', icon: Plug, label: 'Connectors', desc: 'M365 / Salesforce sync' },
    { to: '/admin/integrations', icon: PenTool, label: 'eSignature integrations', desc: 'DocuSign + Adobe Sign' },
    { to: '/admin/integrations/mcp', icon: Bot, label: 'MCP (LLM agents)', desc: 'Claude / Cursor / Copilot tool access' },
    // iPaaS surface (ADR 0090) hidden from the tile grid until external
    // Zapier/Make/n8n apps are actually published. Code + route remain
    // wired so re-enabling is just uncommenting this line.
    // { to: '/admin/integrations/ipaas', icon: Zap, label: 'iPaaS (Zapier / Make / n8n)', desc: 'Issue API keys + see trigger URLs (ADR 0090)' },
    { to: '/admin/privacy', icon: UserCog, label: 'Privacy requests', desc: 'GDPR export / erase / anonymize' },
    { to: '/admin/residency', icon: Globe, label: 'Residency', desc: 'Regions + migrate docs' },
    { to: '/admin/compliance', icon: ShieldAlert, label: 'Compliance', desc: 'Encryption + residency overview' },
    { to: '/admin/billing', icon: CreditCard, label: 'Billing', desc: 'Plan + usage' },
    { to: '/admin/tenant/license', icon: ScrollText, label: 'License', desc: 'JWT claims, seats, feature flags, expiry' },
    { to: '/admin/settings', icon: Settings, label: 'Settings', desc: 'Tenant config' },
    { to: '/admin/bulk', icon: Upload, label: 'Bulk import / export', desc: 'NDJSON migration of workspaces, folders, documents' },
    { to: '/admin/permission-lag', icon: Activity, label: 'Permission propagation', desc: 'Search-index lag p50/p95/p99 vs. 5s SLI' },
  ],
}

// All Intel-Feature 01–10 admin surfaces in one group so they're
// discoverable from the admin landing page rather than hidden behind
// direct URLs only.
const INTEL_GROUP: SectionGroup = {
  label: 'Intelligence',
  description: 'Auto-tag, smart routing, compliance findings, OCR review, and the model + LLM registries.',
  sections: [
    { to: '/admin/intelligence/auto-tag', icon: Sparkles, label: 'Auto-tag config', desc: 'Thresholds + weights for tag suggestions' },
    { to: '/admin/intelligence/tag-review', icon: TagsIcon, label: 'Tag review queue', desc: 'Tenant-wide pending tag suggestions' },
    { to: '/admin/intelligence/routing-rules', icon: MapPinned, label: 'Routing rules', desc: 'Smart-routing rules + config' },
    { to: '/admin/intelligence/filing-analytics', icon: FileSearch, label: 'Filing analytics', desc: 'Suggestion acceptance + filing patterns' },
    { to: '/admin/intelligence/compliance', icon: ShieldAlert, label: 'PII / PHI findings', desc: 'Open compliance findings queue' },
    { to: '/admin/intelligence/compliance-config', icon: Shield, label: 'Compliance config', desc: 'PHI opt-in, custom patterns, thresholds' },
    { to: '/admin/intelligence/ocr-review', icon: FileSearch, label: 'OCR review queue', desc: 'Pages flagged by OCR-quality scoring' },
    { to: '/admin/intelligence/anomalies', icon: AlertTriangle, label: 'Anomaly reports', desc: 'Workspace outlier scans' },
    { to: '/admin/intelligence/models', icon: Brain, label: 'Model registry', desc: 'Per-tenant fine-tuned classifiers' },
    { to: '/admin/intelligence/ner-config', icon: Languages, label: 'NER configuration', desc: 'LLM tier toggle + per-tenant API key' },
    { to: '/admin/intelligence/usage', icon: Activity, label: 'LLM usage', desc: 'Per-tenant token + cost tally across all models' },
    { to: '/admin/tenant/ai', icon: KeyRound, label: 'AI provider', desc: 'Provider, model, fallback, API key, budget' },
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
  const isPlatformAdmin = useAuthStore((s) => s.user?.is_platform_admin === true)
  const groups = [TENANT_GROUP, INTEL_GROUP, ...(isPlatformAdmin ? [PLATFORM_GROUP] : [])]

  return (
    <div className="space-y-10">
      <PageHeader
        title="Administration"
        description="Manage tenant-wide policy, people, and intelligence settings."
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
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {group.sections.map((s) => (
          <SectionCard key={s.to} section={s} />
        ))}
      </div>
    </section>
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
      <Card className="group flex h-full items-start gap-3 p-4 transition-colors hover:bg-accent">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-muted text-foreground transition-colors group-hover:bg-foreground group-hover:text-background">
          <Icon className="h-[1.05rem] w-[1.05rem]" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="flex items-center gap-1 truncate text-sm font-medium">
            {label}
            <DirectionalIcon name="ChevronRight" className="h-3.5 w-3.5 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
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
