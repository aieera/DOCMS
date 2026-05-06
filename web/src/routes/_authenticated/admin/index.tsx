import { createFileRoute, Link } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import {
  Activity, AlertTriangle, Archive, Brain, CreditCard, FileJson, FileSearch, Globe,
  Key, KeyRound, Languages, Link2, MapPinned, Plug, Scale, ScrollText, Settings,
  Shield, ShieldAlert, ShieldCheck, Sparkles, Tag, Tags as TagsIcon,
  UserCog, Users, Webhook, Workflow,
} from 'lucide-react'

interface Section {
  to: string
  icon: typeof Users
  label: string
  desc: string
}

const tenantSections: Section[] = [
  { to: '/admin/users', icon: Users, label: 'Users', desc: 'Manage team members' },
  { to: '/admin/groups', icon: Shield, label: 'Groups', desc: 'Group permissions' },
  { to: '/admin/permissions', icon: ShieldCheck, label: 'Permission Matrix', desc: 'Role × resource grid (read-only)' },
  { to: '/admin/sso', icon: Key, label: 'SSO', desc: 'SAML/OIDC config' },
  { to: '/admin/workflows', icon: Workflow, label: 'Workflows', desc: 'Approval workflows' },
  { to: '/admin/retention', icon: Archive, label: 'Retention', desc: 'Retention policies' },
  { to: '/admin/legal-holds', icon: Scale, label: 'Legal Holds', desc: 'Active holds' },
  { to: '/admin/audit-log', icon: ScrollText, label: 'Audit Log', desc: 'Activity history' },
  { to: '/admin/webhooks', icon: Webhook, label: 'Webhooks', desc: 'Event subscriptions' },
  { to: '/admin/tags', icon: Tag, label: 'Tags', desc: 'Tenant tag catalog' },
  { to: '/admin/metadata-schema', icon: FileJson, label: 'Metadata Schema', desc: 'Custom-field JSON Schema' },
  { to: '/admin/share-links', icon: Link2, label: 'Share Links', desc: 'Active tenant-wide links' },
  { to: '/admin/api-keys', icon: KeyRound, label: 'API Keys', desc: 'Programmatic access' },
  { to: '/admin/connectors', icon: Plug, label: 'Connectors', desc: 'M365 / Salesforce sync' },
  { to: '/admin/privacy', icon: UserCog, label: 'Privacy Requests', desc: 'GDPR export / erase / anonymize' },
  { to: '/admin/residency', icon: Globe, label: 'Residency', desc: 'Regions + migrate docs' },
  { to: '/admin/compliance', icon: ShieldAlert, label: 'Compliance', desc: 'Encryption + residency overview' },
  { to: '/admin/billing', icon: CreditCard, label: 'Billing', desc: 'Plan + usage' },
  { to: '/admin/settings', icon: Settings, label: 'Settings', desc: 'Tenant config' },
  { to: '/admin/permission-lag', icon: Activity, label: 'Permission propagation', desc: 'Search-index lag p50/p95/p99 vs. 5s SLI (ADR 0066)' },
]

// All Intel-Feature 01–10 admin surfaces in one group so they're
// discoverable from the admin landing page rather than hidden behind
// direct URLs only.
const intelligenceSections: Section[] = [
  { to: '/admin/intelligence/auto-tag', icon: Sparkles, label: 'Auto-tag config', desc: 'Thresholds + weights for tag suggestions (ADR 0052)' },
  { to: '/admin/intelligence/tag-review', icon: TagsIcon, label: 'Tag review queue', desc: 'Tenant-wide pending tag suggestions' },
  { to: '/admin/intelligence/routing-rules', icon: MapPinned, label: 'Routing rules', desc: 'Smart-routing rules + config (ADR 0053)' },
  { to: '/admin/intelligence/filing-analytics', icon: FileSearch, label: 'Filing analytics', desc: 'Suggestion acceptance + filing patterns' },
  { to: '/admin/intelligence/compliance', icon: ShieldAlert, label: 'PII/PHI findings', desc: 'Open compliance findings queue (ADR 0054)' },
  { to: '/admin/intelligence/compliance-config', icon: Shield, label: 'Compliance config', desc: 'PHI opt-in, custom patterns, thresholds' },
  { to: '/admin/intelligence/ocr-review', icon: FileSearch, label: 'OCR review queue', desc: 'Pages flagged by OCR-quality scoring (ADR 0057)' },
  { to: '/admin/intelligence/anomalies', icon: AlertTriangle, label: 'Anomaly reports', desc: 'Workspace outlier scans (ADR 0058)' },
  { to: '/admin/intelligence/models', icon: Brain, label: 'Model registry', desc: 'Per-tenant fine-tuned classifiers (ADR 0060)' },
  { to: '/admin/intelligence/ner-config', icon: Languages, label: 'NER configuration', desc: 'LLM tier toggle + per-tenant API key (ADR 0061)' },
  { to: '/admin/intelligence/usage', icon: Activity, label: 'LLM usage', desc: 'Per-tenant token + cost tally across all models' },
  { to: '/admin/tenant/ai', icon: KeyRound, label: 'AI provider', desc: 'Per-tenant LiteLLM routing — provider, model, fallback, key, budget (ADR 0064)' },
]

function AdminPage() {
  return (
    <div>
      <PageHeader title="Administration" description="Manage your VaultDMS tenant" />

      <SectionGrid sections={tenantSections} />

      <h2 className="mt-10 mb-3 text-sm font-semibold uppercase tracking-wide text-[var(--color-text-secondary)]">
        Intelligence
      </h2>
      <SectionGrid sections={intelligenceSections} />
    </div>
  )
}

function SectionGrid({ sections }: { sections: Section[] }) {
  return (
    <div className="grid grid-cols-3 gap-4">
      {sections.map(({ to, icon: Icon, label, desc }) => (
        <Link
          key={to}
          to={to}
          className="flex items-center gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md"
        >
          <Icon className="h-6 w-6 text-[var(--color-primary)]" />
          <div>
            <p className="font-medium">{label}</p>
            <p className="text-xs text-[var(--color-text-secondary)]">{desc}</p>
          </div>
        </Link>
      ))}
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/')({ component: AdminPage })
