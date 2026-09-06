import { createFileRoute, Link } from '@tanstack/react-router'
import { Activity, Database, ShieldAlert, type LucideIcon } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { Card } from '@/components/ui/card'
import { cn } from '@/lib/cn'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { requirePlatformBuild } from '@/lib/platformBuild'

// /admin/platform — hub for cross-tenant platform-admin tools (ADR 0069).
// Members of the `platform_admins` table see this in the /admin grid.
// Without this index file, clicking the "Platform" breadcrumb on any of
// the three sub-pages 404'd.
//
// Cards mirror the PLATFORM_GROUP block from /admin/index.tsx by
// intention — 3 entries is cheaper than abstracting.
interface Section {
  to: string
  icon: LucideIcon
  label: string
  desc: string
}

const SECTIONS: Section[] = [
  { to: '/admin/platform/support-search', icon: ShieldAlert, label: 'Support search', desc: 'Cross-tenant document search — every query audited' },
  { to: '/admin/platform/db-info',        icon: Database,    label: 'Database driver info', desc: 'Active driver + version + capability matrix' },
  { to: '/admin/platform/load-tests',     icon: Activity,    label: 'Load test history',    desc: 'Sign-off runs — index built from docs/load-tests at build time' },
]

function PlatformHubPage() {
  return (
    <div className="space-y-6">
      <PageHeader
        title="Platform administration"
        description="Cross-tenant tools — every action is audited."
      />
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {SECTIONS.map((s) => (
          <SectionCard key={s.to} section={s} />
        ))}
      </div>
    </div>
  )
}

function SectionCard({ section: { to, icon: Icon, label, desc } }: { section: Section }) {
  return (
    <Link
      to={to}
      className={cn(
        'block rounded-lg focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
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

export const Route = createFileRoute('/_authenticated/admin/platform/')({
  beforeLoad: requirePlatformBuild,
  component: PlatformHubPage,
})
