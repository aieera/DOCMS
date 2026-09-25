import { createFileRoute, Link } from '@tanstack/react-router'
import { Bell, LifeBuoy, ShieldCheck, Smartphone } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'

import { PageHeader } from '@/components/shared/PageHeader'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { Avatar } from '@/components/ui/shadcn/avatar'
import { Badge } from '@/components/ui/shadcn/badge'
import { useCurrentUser } from '@/hooks/useAuth'
import { formatDate, formatRelativeTime } from '@/lib/formatters'
import type { User } from '@/types/api'

// /settings — landing hub for the personal settings area.
//
// Previously this URL fell through to the global 404 because only the
// leaf routes (/settings/security, /settings/notifications, …) existed.
// This page gives the area a front door.
//
// It shows the account as well as the doors to it. Everything on the
// left comes from useCurrentUser(), which /auth/me has already filled
// in -- no query is issued here. A settings landing page that cannot
// answer "is my two-factor on?" is just the sidebar again.

interface SectionRow {
  to: string
  icon: LucideIcon
  tint: string
  title: string
  description: string
  testId: string
  // Only two-factor has live state in the store. The rest would need
  // their own requests (recovery-code count, per-channel prefs) and a
  // hub is not worth a fetch, so they carry no badge rather than a
  // guessed one.
  status?: (u: User | null) => { label: string; variant: 'active' | 'in_review' } | undefined
}

const GROUPS: { heading: string; rows: SectionRow[] }[] = [
  {
    heading: 'Security',
    rows: [
      {
        to: '/settings/security',
        icon: ShieldCheck,
        tint: 'text-blue-600 dark:text-blue-400',
        title: 'Account & security',
        description: 'Your profile, passkeys, and active sessions.',
        testId: 'settings-card-security',
      },
      {
        to: '/settings/security/mfa',
        icon: Smartphone,
        tint: 'text-emerald-600 dark:text-emerald-400',
        title: 'Two-factor authentication',
        description: 'An authenticator app for sign-in codes.',
        testId: 'settings-card-mfa',
        status: (u) =>
          u?.mfa_enabled
            ? { label: 'On', variant: 'active' }
            : { label: 'Off', variant: 'in_review' },
      },
      {
        to: '/settings/security/mfa/recovery',
        icon: LifeBuoy,
        tint: 'text-amber-600 dark:text-amber-400',
        title: 'Recovery codes',
        description: 'One-time backup codes for when you lose your authenticator.',
        testId: 'settings-card-recovery',
      },
    ],
  },
  {
    heading: 'Preferences',
    rows: [
      {
        to: '/settings/notifications',
        icon: Bell,
        tint: 'text-violet-600 dark:text-violet-400',
        title: 'Notification preferences',
        description: 'Which events reach you, per channel, with digests, snoozes, and quiet hours.',
        testId: 'settings-card-notifications',
      },
    ],
  },
]

const ROLE_LABELS: Record<User['role'], string> = {
  owner: 'Owner',
  admin: 'Admin',
  member: 'Member',
  guest: 'Guest',
  compliance_officer: 'Compliance officer',
}

function SettingsIndexPage() {
  const user = useCurrentUser()

  return (
    // No padding or max-width of its own: app-layout already wraps every
    // page in `p-4 sm:p-6 lg:p-8`, and the old `mx-auto max-w-3xl p-6`
    // both double-padded and pinned 768px of content in the middle of a
    // 1660px column.
    <div>
      <PageHeader
        title="Settings"
        description="Manage your account, security, and notification preferences."
      />

      <div className="grid gap-6 lg:grid-cols-[minmax(280px,340px)_minmax(0,1fr)]">
        <IdentityCard user={user} />

        <div className="min-w-0 space-y-6">
          {GROUPS.map(({ heading, rows }) => (
            <section key={heading} aria-label={heading}>
              <h2
                dir="auto"
                className="mb-2 px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground"
              >
                {heading}
              </h2>
              <ul className="divide-y divide-border overflow-hidden rounded-lg border border-border bg-card">
                {rows.map((row) => (
                  <SectionLink key={row.to} row={row} user={user} />
                ))}
              </ul>
            </section>
          ))}
        </div>
      </div>
    </div>
  )
}

function IdentityCard({ user }: { user: User | null }) {
  const name = user?.display_name || user?.email || 'Your account'
  return (
    <aside
      aria-label="Your account"
      className="h-fit rounded-lg border border-border bg-card p-5 lg:sticky lg:top-6"
      data-testid="settings-identity"
    >
      <div className="flex flex-col items-center text-center">
        <Avatar src={user?.avatar_url} name={name} className="h-16 w-16 text-xl" />
        <p dir="auto" className="mt-3 w-full truncate text-base font-semibold text-foreground">
          {name}
        </p>
        {user?.email && (
          // dir=ltr: an address is never bidi-reordered, and inside the
          // Arabic UI "@artiflexit.com" would otherwise jump to the front.
          <p dir="ltr" className="w-full truncate text-sm text-muted-foreground">
            {user.email}
          </p>
        )}
        {user?.status && user.status !== 'active' && (
          <Badge variant="in_review" className="mt-2 capitalize">
            {user.status}
          </Badge>
        )}
      </div>

      <dl className="mt-5 space-y-3 border-t border-border pt-4 text-sm">
        {/* Two-factor is deliberately NOT repeated here. It is already a
            badge on its own row to the right, next to the control that
            changes it, and saying it twice on one screen is the kind of
            duplication this redesign exists to remove. */}
        <Fact label="Role">{user ? ROLE_LABELS[user.role] : '—'}</Fact>
        <Fact label="Language">{user?.locale === 'ar' ? 'العربية' : 'English'}</Fact>
        {user?.created_at && <Fact label="Member since">{formatDate(user.created_at)}</Fact>}
        {user?.last_login_at && (
          <Fact label="Last sign-in">{formatRelativeTime(user.last_login_at)}</Fact>
        )}
      </dl>
    </aside>
  )
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-3">
      <dt className="w-28 shrink-0 text-xs uppercase tracking-wide text-muted-foreground">
        {label}
      </dt>
      <dd dir="auto" className="min-w-0 flex-1 truncate text-foreground">
        {children}
      </dd>
    </div>
  )
}

function SectionLink({ row, user }: { row: SectionRow; user: User | null }) {
  const { to, icon: Icon, tint, title, description, testId } = row
  const status = row.status?.(user)
  return (
    <li>
      <Link
        to={to}
        data-testid={testId}
        className={
          'group flex items-center gap-3 p-4 transition-colors hover:bg-muted/40 ' +
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring'
        }
      >
        <Icon className={`h-5 w-5 shrink-0 ${tint}`} aria-hidden />
        {/* dir="auto" ONCE, on the block that holds both lines. These
            strings are English and stay English while the UI is Arabic,
            so without it the bidi algorithm throws their trailing full
            stop to the front -- ".Your profile, passkeys, and active
            sessions". Putting it on the title and the description
            separately also works for the punctuation but resolves each
            one independently, which left the title aligned to the right
            and its own description to the left. The RTL geometry gate
            catches neither: nothing moves, the characters reorder. */}
        <span dir="auto" className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-2">
            <span className="text-sm font-semibold text-foreground">{title}</span>
            {status && (
              <Badge variant={status.variant} // Not `${testId}-status`: that nests inside the settings-card-*
                // prefix, so a [data-testid^="settings-card-"] query returns the
                // badge alongside the links it is meant to count.
                data-testid={`settings-status-${testId.replace('settings-card-', '')}`}>
                {status.label}
              </Badge>
            )}
          </span>
          <span className="mt-0.5 block text-sm text-muted-foreground">{description}</span>
        </span>
        <DirectionalIcon
          name="ChevronRight"
          className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5 rtl:rotate-180"
          aria-hidden
        />
      </Link>
    </li>
  )
}

export const Route = createFileRoute('/_authenticated/settings/')({ component: SettingsIndexPage })
