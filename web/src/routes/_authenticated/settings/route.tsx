import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { Bell, PenLine, ShieldCheck } from 'lucide-react'
import { cn } from '@/lib/cn'
import { PageHeader } from '@/components/shared/PageHeader'

const tabs = [
  { to: '/settings/sessions', icon: ShieldCheck, label: 'Active sessions' },
  { to: '/settings/notifications', icon: Bell, label: 'Notifications' },
  { to: '/settings/signatures', icon: PenLine, label: 'Signatures' },
] as const

function SettingsLayout() {
  const { pathname } = useLocation()
  return (
    <div className="space-y-6">
      <PageHeader title="Account settings" description="Personal security, notifications, and connected integrations." />
      <div className="grid grid-cols-[220px_1fr] gap-8">
        <nav aria-label="Settings sections" className="space-y-1">
          {tabs.map(({ to, icon: Icon, label }) => {
            const active = pathname.startsWith(to)
            return (
              <Link
                key={to}
                to={to}
                data-testid={`settings-nav-${to.split('/').pop()}`}
                className={cn(
                  'flex items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors',
                  active
                    ? 'bg-[var(--color-accent)] font-medium text-[var(--color-text)]'
                    : 'text-[var(--color-text-secondary)] hover:bg-slate-100 hover:text-[var(--color-text)] dark:hover:bg-slate-800',
                )}
              >
                <Icon className="h-4 w-4 shrink-0" />
                <span>{label}</span>
              </Link>
            )
          })}
        </nav>
        <div className="min-w-0">
          <Outlet />
        </div>
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/settings')({ component: SettingsLayout })
