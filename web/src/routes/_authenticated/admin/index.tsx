import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
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
  labelKey: string
  descKey: string
}

interface SubGroup {
  labelKey: string
  sections: Section[]
}

interface SectionGroup {
  labelKey: string
  descKey: string
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
  labelKey: 'admin.group.tenant',
  descKey: 'admin.group.tenant_desc',
  subgroups: [
    {
      labelKey: 'admin.group.people',
      sections: [
        { to: '/admin/identity', icon: Users, labelKey: 'admin.card.admin_identity.label', descKey: 'admin.card.admin_identity.desc' },
        { to: '/admin/api-keys', icon: KeyRound, labelKey: 'admin.card.admin_api_keys.label', descKey: 'admin.card.admin_api_keys.desc' },
        { to: '/admin/share-links', icon: Link2, labelKey: 'admin.card.admin_share_links.label', descKey: 'admin.card.admin_share_links.desc' },
      ],
    },
    {
      labelKey: 'admin.group.security',
      sections: [
        { to: '/admin/protection', icon: ShieldCheck, labelKey: 'admin.card.admin_protection.label', descKey: 'admin.card.admin_protection.desc' },
        { to: '/admin/tenant/encryption', icon: Lock, labelKey: 'admin.card.admin_tenant_encryption.label', descKey: 'admin.card.admin_tenant_encryption.desc' },
        { to: '/admin/tenant/sync', icon: FolderSync, labelKey: 'admin.card.admin_tenant_sync.label', descKey: 'admin.card.admin_tenant_sync.desc' },
      ],
    },
    {
      labelKey: 'admin.group.compliance',
      sections: [
        { to: '/admin/records-retention', icon: FolderTree, labelKey: 'admin.card.admin_records_retention.label', descKey: 'admin.card.admin_records_retention.desc' },
        { to: '/admin/legal', icon: Scale, labelKey: 'admin.card.admin_legal.label', descKey: 'admin.card.admin_legal.desc' },
        { to: '/admin/privacy', icon: UserCog, labelKey: 'admin.card.admin_privacy.label', descKey: 'admin.card.admin_privacy.desc' },
        { to: '/admin/audit', icon: ScrollText, labelKey: 'admin.card.admin_audit.label', descKey: 'admin.card.admin_audit.desc' },
        { to: '/admin/data-governance', icon: ShieldAlert, labelKey: 'admin.card.admin_data_governance.label', descKey: 'admin.card.admin_data_governance.desc' },
      ],
    },
    {
      labelKey: 'admin.group.config',
      sections: [
        { to: '/admin/subscription', icon: CreditCard, labelKey: 'admin.card.admin_subscription.label', descKey: 'admin.card.admin_subscription.desc' },
        { to: '/admin/tenant-settings', icon: Settings, labelKey: 'admin.card.admin_tenant_settings.label', descKey: 'admin.card.admin_tenant_settings.desc' },
        { to: '/admin/metadata-schema', icon: FileJson, labelKey: 'admin.card.admin_metadata_schema.label', descKey: 'admin.card.admin_metadata_schema.desc' },
        { to: '/admin/workflows', icon: Workflow, labelKey: 'admin.card.admin_workflows.label', descKey: 'admin.card.admin_workflows.desc' },
        // /templates has no sidebar entry (removed 2026-07-28 by request)
        // and nothing else linked to it, so the page was reachable only
        // by typing the URL. Provisioning a workspace from a template is
        // a setup task, so the admin hub is where it belongs.
        { to: '/templates', icon: LayoutTemplate, labelKey: 'admin.card.templates.label', descKey: 'admin.card.templates.desc' },
        { to: '/admin/bulk', icon: Upload, labelKey: 'admin.card.admin_bulk.label', descKey: 'admin.card.admin_bulk.desc' },
        { to: '/admin/permission-lag', icon: Activity, labelKey: 'admin.card.admin_permission_lag.label', descKey: 'admin.card.admin_permission_lag.desc' },
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
  labelKey: 'admin.group.integrations',
  descKey: 'admin.group.integrations_desc',
  sections: [
    { to: '/admin/integrations', icon: Plug, labelKey: 'admin.card.admin_integrations.label', descKey: 'admin.card.admin_integrations.desc' },
  ],
}

// Intelligence surfaces — 4 consolidated tiles + 3 standalone-kept
// (Routing rules, Filing analytics, Anomaly reports). The merged
// /admin/intelligence/* URLs still resolve for back-compat.
const INTEL_GROUP: SectionGroup = {
  labelKey: 'admin.group.intel',
  descKey: 'admin.group.intel_desc',
  sections: [
    { to: '/admin/ai', icon: Brain, labelKey: 'admin.card.admin_ai.label', descKey: 'admin.card.admin_ai.desc' },
    { to: '/admin/tagging', icon: TagsIcon, labelKey: 'admin.card.admin_tagging.label', descKey: 'admin.card.admin_tagging.desc' },
    { to: '/admin/ocr', icon: FileSearch, labelKey: 'admin.card.admin_ocr.label', descKey: 'admin.card.admin_ocr.desc' },
    { to: '/admin/ingestion', icon: Upload, labelKey: 'admin.card.admin_ingestion.label', descKey: 'admin.card.admin_ingestion.desc' },
    { to: '/admin/pii-scanning', icon: Shield, labelKey: 'admin.card.admin_pii_scanning.label', descKey: 'admin.card.admin_pii_scanning.desc' },
    { to: '/admin/intelligence/routing-rules', icon: MapPinned, labelKey: 'admin.card.admin_intelligence_routing_rules.label', descKey: 'admin.card.admin_intelligence_routing_rules.desc' },
    { to: '/admin/intelligence/filing-analytics', icon: FileSearch, labelKey: 'admin.card.admin_intelligence_filing_analytics.label', descKey: 'admin.card.admin_intelligence_filing_analytics.desc' },
    { to: '/admin/intelligence/anomalies', icon: AlertTriangle, labelKey: 'admin.card.admin_intelligence_anomalies.label', descKey: 'admin.card.admin_intelligence_anomalies.desc' },
  ],
}

// ADR 0069 — platform-admin-only sections. Membership is in the
// `platform_admins` table, separate from a user's tenant role.
const PLATFORM_GROUP: SectionGroup = {
  labelKey: 'admin.group.platform',
  descKey: 'admin.group.platform_desc',
  sections: [
    { to: '/admin/platform/support-search', icon: ShieldAlert, labelKey: 'admin.card.admin_platform_support_search.label', descKey: 'admin.card.admin_platform_support_search.desc' },
    { to: '/admin/platform/db-info', icon: Database, labelKey: 'admin.card.admin_platform_db_info.label', descKey: 'admin.card.admin_platform_db_info.desc' },
    { to: '/admin/platform/load-tests', icon: Activity, labelKey: 'admin.card.admin_platform_load_tests.label', descKey: 'admin.card.admin_platform_load_tests.desc' },
  ],
}

function AdminPage() {
  const { t } = useTranslation('common')
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
        title={t('admin.title')}
        description={
          isComplianceOfficer
            ? t('admin.description_co')
            : t('admin.description')
        }
      />
      {groups.map((g) => (
        <SectionGroupBlock key={g.labelKey} group={g} />
      ))}
    </div>
  )
}

function SectionGroupBlock({ group }: { group: SectionGroup }) {
  const { t } = useTranslation('common')
  return (
    <section aria-labelledby={`group-${slugify(group.labelKey)}`}>
      <div className="mb-4 border-b border-border pb-2">
        <h2
          id={`group-${slugify(group.labelKey)}`}
          className="text-xs font-semibold uppercase tracking-wider text-muted-foreground"
        >
          {t(group.labelKey)}
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">{t(group.descKey)}</p>
      </div>
      {group.subgroups ? (
        <div className="space-y-6">
          {group.subgroups.map((sg) => (
            <div key={sg.labelKey}>
              <h3 className="mb-3 text-sm font-medium text-foreground">{t(sg.labelKey)}</h3>
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

function SectionCard({ section: { to, icon: Icon, labelKey, descKey } }: { section: Section }) {
  const { t } = useTranslation('common')
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
            <span className="truncate">{t(labelKey)}</span>
            <DirectionalIcon name="ChevronRight" className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 group-hover:text-foreground" />
          </p>
          <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{t(descKey)}</p>
        </div>
      </Card>
    </Link>
  )
}

function slugify(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
}

export const Route = createFileRoute('/_authenticated/admin/')({ component: AdminPage })
