import { createFileRoute, Link } from '@tanstack/react-router'
import { Activity, AlertTriangle, Archive, Brain, CreditCard, FileJson, FileKey, FileSearch, FolderSync, FolderTree, KeyRound, Link2, Lock, MapPinned, Plug, Radio, Scale, ScrollText, Settings, Shield, ShieldAlert, ShieldCheck, Stamp, Tags as TagsIcon, Upload, UserCog, Users, Workflow, Database, type LucideIcon } from 'lucide-react'
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

// 2026-05-29 admin-consolidation:
// Merged tiles replace their per-page predecessors. The standalone
// keep-list (License, Billing, Workflows, Retention, Legal holds,
// Audit log, Metadata schema, Share links, API keys, Privacy requests,
// Bulk, Permission propagation) stays as-is — only navigation grouping
// changes. Old URLs (/admin/users, /admin/compliance, etc.) still
// resolve and render the same component for bookmark compatibility.
const TENANT_GROUP: SectionGroup = {
  label: 'Tenant administration',
  description: 'People, policy, and tenant-wide configuration.',
  sections: [
    { to: '/admin/identity', icon: Users, label: 'Identity & Access', desc: 'Users, groups, roles, SSO, LDAP/AD' },
    { to: '/admin/scim', icon: UserCog, label: 'SCIM provisioning', desc: 'IdP user/group provisioning + deprovision + log' },
    { to: '/admin/tenant-settings', icon: Settings, label: 'Tenant settings', desc: 'Feature flags + upload policy' },
    { to: '/admin/data-governance', icon: ShieldAlert, label: 'Data governance', desc: 'Compliance overview + residency migrations' },
    { to: '/admin/tenant/license', icon: ScrollText, label: 'License', desc: 'JWT claims, seats, feature flags, expiry' },
    { to: '/admin/tenant/classification', icon: ShieldCheck, label: 'Classification & access', desc: 'Sensitivity rules · per-user clearance · PHI/PII gating' },
    { to: '/admin/tenant/watermark', icon: Stamp, label: 'Watermark', desc: 'Dynamic viewer watermark — template, opacity, tiling, per-classification' },
    { to: '/admin/tenant/irm', icon: FileKey, label: 'Protected exports', desc: 'IRM licenses — recipients, expiry, opens, revoke' },
    { to: '/admin/tenant/sync', icon: FolderSync, label: 'Devices & Sync', desc: 'Selective-sync devices, folders, status, revoke' },
    { to: '/admin/tenant/encryption', icon: Lock, label: 'Encryption & keys', desc: 'External KMS (Vault/AWS/Azure) · rotation · break-glass revoke' },
    { to: '/admin/billing', icon: CreditCard, label: 'Billing', desc: 'Plan + usage' },
    { to: '/admin/workflows', icon: Workflow, label: 'Workflows', desc: 'Approval workflows' },
    { to: '/admin/retention', icon: Archive, label: 'Retention', desc: 'Retention policies' },
    { to: '/admin/legal-holds', icon: Scale, label: 'Legal holds', desc: 'Active holds' },
    { to: '/admin/ediscovery', icon: FileSearch, label: 'E-discovery', desc: 'Hold-scoped export: docs + metadata + audit' },
    { to: '/admin/records', icon: FolderTree, label: 'Records', desc: 'File plan · retention schedules · disposition' },
    { to: '/admin/audit-log', icon: ScrollText, label: 'Audit log', desc: 'Activity history' },
    { to: '/admin/siem', icon: Radio, label: 'SIEM forwarding', desc: 'Forward events to syslog / Splunk / Sentinel' },
    { to: '/admin/metadata-schema', icon: FileJson, label: 'Metadata schema', desc: 'Custom-field JSON Schema' },
    { to: '/admin/share-links', icon: Link2, label: 'Share links', desc: 'Active tenant-wide links' },
    { to: '/admin/api-keys', icon: KeyRound, label: 'API keys', desc: 'Programmatic access' },
    { to: '/admin/privacy', icon: UserCog, label: 'Privacy requests', desc: 'GDPR export / erase / anonymize' },
    { to: '/admin/bulk', icon: Upload, label: 'Bulk import / export', desc: 'NDJSON migration of workspaces, folders, documents' },
    { to: '/admin/permission-lag', icon: Activity, label: 'Permission propagation', desc: 'Search-index lag p50/p95/p99 vs. 5s SLI' },
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
  const isPlatformAdmin = useAuthStore((s) => s.user?.is_platform_admin === true)
  const groups = [TENANT_GROUP, INTEGRATIONS_GROUP, INTEL_GROUP, ...(isPlatformAdmin ? [PLATFORM_GROUP] : [])]

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
      {/* Card hover: a subtle primary-tinted border + slight bg lift,
          matching the workspace cards' pattern so the admin grid feels
          part of the same product. Icon sits in a fixed 40px tile with
          a primary-tinted background that intensifies on hover. */}
      <Card className="group flex h-full items-start gap-3 border p-4 transition-all hover:border-primary/40 hover:bg-accent/50 hover:shadow-sm">
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
