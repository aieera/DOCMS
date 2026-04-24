import { createFileRoute, Link } from '@tanstack/react-router'
import { PageHeader } from '@/components/shared/PageHeader'
import { Users, Shield, Key, Workflow, Archive, Scale, ScrollText, Webhook, Settings, Tag, Link2, ShieldCheck, KeyRound, Plug, CreditCard, ShieldAlert, UserCog, Globe, FileJson, ClipboardCheck, MapPin, Activity, Clock } from 'lucide-react'

const sections = [
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
  { to: '/admin/acknowledgements', icon: ClipboardCheck, label: 'Acknowledgements', desc: 'Policy attestation campaigns' },
  { to: '/admin/geofences', icon: MapPin, label: 'Geofences', desc: 'Country + CIDR access policies' },
  { to: '/admin/platform/internal-auth', icon: Activity, label: 'Internal Auth', desc: 'mTLS + HMAC health per service' },
  { to: '/admin/platform/schedules', icon: Clock, label: 'Schedules', desc: 'Temporal schedules (password / ack / signature sweeps)' },
  { to: '/admin/settings', icon: Settings, label: 'Settings', desc: 'Tenant config' },
] as const

function AdminPage() {
  return (
    <div>
      <PageHeader title="Administration" description="Manage your VaultDMS tenant" />
      <div className="grid grid-cols-3 gap-4">
        {sections.map(({ to, icon: Icon, label, desc }) => (
          <Link key={to} to={to} className="flex items-center gap-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-secondary)] p-4 transition-shadow hover:shadow-md">
            <Icon className="h-6 w-6 text-[var(--color-primary)]" />
            <div>
              <p className="font-medium">{label}</p>
              <p className="text-xs text-[var(--color-text-secondary)]">{desc}</p>
            </div>
          </Link>
        ))}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/admin/')({ component: AdminPage })
