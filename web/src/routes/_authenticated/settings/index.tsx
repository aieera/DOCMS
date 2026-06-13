import { createFileRoute, Link } from '@tanstack/react-router'
import { Bell, ChevronRight, LifeBuoy, ShieldCheck, Smartphone } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'

// /settings — landing hub for the personal settings area.
//
// Previously this URL fell through to the global 404 because only
// the leaf routes (/settings/security, /settings/notifications, …)
// existed. Users who trimmed the URL or typed /settings by hand got
// "Page not found" for a section that clearly exists. This page
// gives the area a front door: one card per leaf route, grouped and
// described, so the sections are discoverable without the sidebar.

interface SectionCard {
  to: string
  icon: LucideIcon
  tint: string
  title: string
  description: string
  testId: string
}

const SECTIONS: SectionCard[] = [
  {
    to: '/settings/security',
    icon: ShieldCheck,
    tint: 'bg-blue-500/10 text-blue-600 dark:text-blue-400',
    title: 'Account & security',
    description: 'Your profile, passkeys, and active sessions.',
    testId: 'settings-card-security',
  },
  {
    to: '/settings/security/mfa',
    icon: Smartphone,
    tint: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
    title: 'Two-factor authentication',
    description: 'Set up an authenticator app for sign-in codes.',
    testId: 'settings-card-mfa',
  },
  {
    to: '/settings/security/mfa/recovery',
    icon: LifeBuoy,
    tint: 'bg-amber-500/10 text-amber-600 dark:text-amber-400',
    title: 'Recovery codes',
    description: 'One-time backup codes for when you lose your authenticator.',
    testId: 'settings-card-recovery',
  },
  {
    to: '/settings/notifications',
    icon: Bell,
    tint: 'bg-violet-500/10 text-violet-600 dark:text-violet-400',
    title: 'Notification preferences',
    description: 'Choose which events reach you, per channel, with digests, snoozes, and quiet hours.',
    testId: 'settings-card-notifications',
  },
]

function SettingsIndexPage() {
  return (
    <div className="mx-auto max-w-3xl p-6">
      <PageHeader
        title="Settings"
        description="Manage your account, security, and notification preferences."
      />
      <div className="grid gap-4 sm:grid-cols-2">
        {SECTIONS.map(({ to, icon: Icon, tint, title, description, testId }) => (
          <Link
            key={to}
            to={to}
            data-testid={testId}
            className={
              'group flex items-start gap-3 rounded-lg border border-border bg-card p-4 ' +
              'transition-colors hover:border-ring/60 hover:bg-muted/40 ' +
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
            }
          >
            <span
              className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-full ${tint}`}
              aria-hidden
            >
              <Icon className="h-5 w-5" />
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center justify-between gap-2">
                <span className="text-sm font-semibold text-foreground">{title}</span>
                <ChevronRight
                  className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 rtl:rotate-180"
                  aria-hidden
                />
              </span>
              <span className="mt-1 block text-sm text-muted-foreground">{description}</span>
            </span>
          </Link>
        ))}
      </div>
    </div>
  )
}

export const Route = createFileRoute('/_authenticated/settings/')({ component: SettingsIndexPage })
