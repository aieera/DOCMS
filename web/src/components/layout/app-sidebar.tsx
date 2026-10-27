import type { ReactNode } from 'react'
import { Link, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useDirection } from '@/hooks/useDirection'
import { LayoutDashboard, CheckSquare, Trash2, Settings, FolderOpen, Boxes, Sparkles, BookOpen, UserCog, Lock, Users, Globe, Inbox, type LucideIcon , BarChart3 } from 'lucide-react'
import { cn } from '@/lib/cn'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/shadcn/sheet'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { listSmartFolders, type SavedSearch, type TreeVisibility } from '@/api/savedSearches'
import { LanguageSelector } from '@/components/shared/LanguageSelector'
import { BrandMark } from './brand-mark'

interface NavItem {
  to: string
  icon: LucideIcon
  // i18n key under common.sidebar.*. The English copy in
  // public/locales/en/common.json is the source of truth.
  labelKey: string
  // exact = false means "/admin" matches /admin/users too
  exact?: boolean
  // Restrict the link to a set of roles. Omitted = visible to all.
  // The backend already 403s admin endpoints for non-admin roles, so
  // this is purely a UX guard — but without it a Member sees an
  // empty Admin shell that looks broken (BUG-B).
  roles?: string[]
}

interface NavGroup {
  labelKey: string
  items: NavItem[]
}

const NAV_GROUPS: NavGroup[] = [
  {
    labelKey: 'sidebar.workspace',
    items: [
      { to: '/', icon: LayoutDashboard, labelKey: 'sidebar.dashboard', exact: true },
      { to: '/workspaces', icon: FolderOpen, labelKey: 'sidebar.workspaces' },
      // Cross-workspace surface for folders shared with the caller via
      // folder_grants (direct or group). Hidden from admins is NOT a
      // goal — admins see all so they'd rarely need this, but it's
      // safe + cheap to show universally.
      { to: '/shared-with-me', icon: Inbox, labelKey: 'sidebar.shared_with_me' },
      // /search entry removed 2026-05-29 — search is reached via the
      // header search bar (Cmd+K). Result page still lives at /search.
      { to: '/ask', icon: Sparkles, labelKey: 'sidebar.ask' },
      // Saved searches moved into the header search dropdown (recents +
      // saved, star-to-save). The /saved-searches management page still
      // exists for alerts/subscribers/smart folders — linked from the
      // dropdown footer, no longer top-level nav.
      // ADR 0104 — clause library.
      { to: '/clauses',        icon: BookOpen,  labelKey: 'sidebar.clauses' },
      // ADR 0118 workspace-templates gallery dropped from nav
      // 2026-07-28 (user request) — /templates route still resolves
      // for deep links.
      // ADR 0119 — analytics report builder (admin-gated backend-side).
      { to: '/reports',        icon: BarChart3, labelKey: 'sidebar.reports', roles: ['admin', 'owner'] },
    ],
  },
  {
    // Native ERP↔SeDoc integration surface — replaces the old embedded
    // customer-files iframe. Its own top-level section so further
    // integrations can slot in beside ERP. Admin/owner only.
    labelKey: 'sidebar.integrations',
    items: [
      { to: '/integrations', icon: Boxes, labelKey: 'sidebar.integrations', roles: ['admin', 'owner'] },
    ],
  },
  {
    labelKey: 'sidebar.inbox',
    items: [
      { to: '/tasks', icon: CheckSquare, labelKey: 'sidebar.tasks' },
      { to: '/trash', icon: Trash2, labelKey: 'sidebar.trash' },
    ],
  },
  {
    labelKey: 'sidebar.settings',
    items: [
      { to: '/settings/security', icon: UserCog, labelKey: 'sidebar.my_settings' },
      // compliance_officer gets a filtered hub (intelligence read-only
      // pages the backend authorizes) — see COMPLIANCE_OFFICER_ADMIN_PATHS.
      { to: '/admin', icon: Settings, labelKey: 'sidebar.admin', roles: ['admin', 'owner', 'compliance_officer'] },
    ],
  },
]

function isActive(pathname: string, item: NavItem) {
  if (item.exact) return pathname === item.to
  if (item.to === '/') return pathname === '/'
  return pathname === item.to || pathname.startsWith(item.to + '/')
}

// Shared item geometry for every row in the rail (nav links and smart
// folders): 32px tall, 8px radius, 14px text, 8px icon gap. Active and
// hover share one surface (`sidebar-accent`); focus is the 1px ring plus
// the shared 4px halo token, exactly as the Button paints it.
const NAV_ITEM_BASE = cn(
  'flex h-8 items-center gap-2 rounded-md px-2 text-sm transition-colors duration-150',
  'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:ring-offset-0',
  'focus-visible:shadow-focus-halo',
)
const NAV_ITEM_IDLE = 'text-sidebar-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground'
const NAV_ITEM_ACTIVE = 'bg-sidebar-accent text-sidebar-accent-foreground'

// 32px group-label row (12px medium, muted). Shared by the nav groups and
// the smart-folders block so both headings line up.
const NAV_LABEL = 'flex h-8 items-center gap-1.5 px-2 text-xs font-medium text-muted-foreground'

function NavLink({ item, collapsed, pathname }: { item: NavItem; collapsed: boolean; pathname: string }) {
  const { t } = useTranslation('common')
  const active = isActive(pathname, item)
  const Icon = item.icon
  const label = t(item.labelKey)
  return (
    <Link
      to={item.to}
      title={collapsed ? label : undefined}
      aria-label={label}
      aria-current={active ? 'page' : undefined}
      className={cn(NAV_ITEM_BASE, active ? NAV_ITEM_ACTIVE : NAV_ITEM_IDLE, collapsed && 'justify-center px-0')}
    >
      <Icon className="h-4 w-4 shrink-0" />
      {!collapsed && <span className="truncate">{label}</span>}
    </Link>
  )
}

// The collapse/expand control is not here: it lives in the header
// (app-topbar.tsx), as does the mobile drawer's opener; the sheet closes
// through its own control. Collapsed, the row keeps just the brand tile.
function BrandRow({ collapsed }: { collapsed: boolean }) {
  const { t } = useTranslation('common')
  return (
    <div className={cn('flex h-14 shrink-0 items-center border-b border-sidebar-border px-3', collapsed && 'justify-center px-2')}>
      <Link
        to="/"
        aria-label={collapsed ? 'SeDoc' : undefined}
        className="flex min-w-0 items-center gap-2 rounded-md focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring focus-visible:shadow-focus-halo"
      >
        <BrandMark />
        {!collapsed && (
          <span className="flex min-w-0 flex-col">
            <span className="truncate text-sm font-semibold leading-[18px] text-sidebar-foreground">SeDoc</span>
            <span className="truncate text-xs leading-4 text-muted-foreground">{t('sidebar.tagline')}</span>
          </span>
        )}
      </Link>
    </div>
  )
}

function NavGroupBlock({
  group,
  collapsed,
  pathname,
  isFirst,
  pinToBottom,
  children,
}: {
  group: NavGroup
  collapsed: boolean
  pathname: string
  isFirst: boolean
  // Pushes the group to the bottom of the rail (margin-top: auto). The
  // rail is a flex column, so this only has an effect when the nav has
  // spare height; on short viewports the nav scrolls instead.
  pinToBottom: boolean
  children?: ReactNode
}) {
  const { t } = useTranslation('common')
  const label = t(group.labelKey)
  return (
    // role=group + aria-label keeps the grouping available to assistive
    // tech in collapsed mode too, where the visible label row is gone.
    <div
      role="group"
      aria-label={label}
      className={cn(
        'space-y-0.5',
        // Collapsed: no label rows, so a hairline separates the groups.
        collapsed && !isFirst && 'border-t border-sidebar-border pt-2',
        pinToBottom && 'mt-auto',
      )}
    >
      {!collapsed && (
        <div aria-hidden="true" className={NAV_LABEL}>
          {label}
        </div>
      )}
      {group.items.map((item) => (
        <NavLink key={item.to} item={item} collapsed={collapsed} pathname={pathname} />
      ))}
      {children}
    </div>
  )
}

// ADR 0100 — smart folders block in the main sidebar. Shows a small
// section under the Workspace group with each smart folder linking to
// /search with the saved query prefilled. Hidden entirely when the
// caller has no smart folders or the sidebar is collapsed.
function SmartFoldersBlock({ collapsed }: { collapsed: boolean }) {
  const { t } = useTranslation('common')
  const { data } = useQuery({
    queryKey: ['smart-folders'],
    queryFn: listSmartFolders,
    staleTime: 30_000,
    // Don't surface fetch errors here — empty list is the safe default,
    // and a transient 4xx during the post-login race shouldn't crash
    // the sidebar.
    retry: false,
  })
  if (collapsed) return null
  if (!data || data.length === 0) return null
  return (
    <div className="space-y-0.5">
      <div className={NAV_LABEL}>
        <Sparkles className="h-3 w-3" />
        {t('sidebar.smart_folders')}
      </div>
      {data.map((sf) => <SmartFolderLink key={sf.id} sf={sf} />)}
    </div>
  )
}

function SmartFolderLink({ sf }: { sf: SavedSearch }) {
  const Vis = visibilityIcon(sf.tree_visibility)
  return (
    <Link
      to="/search"
      search={{ q: sf.query, saved: sf.id } as any}
      className={cn(NAV_ITEM_BASE, NAV_ITEM_IDLE)}
      data-testid={`smart-folder-${sf.id}`}
    >
      <Sparkles className="h-4 w-4 shrink-0" />
      <span className="truncate">{sf.name}</span>
      <Vis className="ms-auto h-3 w-3 shrink-0 opacity-60" aria-label={sf.tree_visibility ?? 'private'} />
    </Link>
  )
}

function visibilityIcon(v?: TreeVisibility) {
  if (v === 'workspace') return Users
  if (v === 'public') return Globe
  return Lock
}

export function SidebarContent({ collapsed, footer }: {
  collapsed: boolean
  /** A row pinned under the nav (the mobile sheet's language control). */
  footer?: ReactNode
}) {
  const { t } = useTranslation('common')
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const role = useAuthStore((s) => s.user?.role ?? '')
  const isPlatformAdmin = useAuthStore((s) => s.user?.is_platform_admin === true)
  const visibleGroups = NAV_GROUPS
    .map((g) => ({
      ...g,
      items: g.items.filter(
        (i) => !i.roles || i.roles.includes(role) || isPlatformAdmin,
      ),
    }))
    .filter((g) => g.items.length > 0)
  return (
    <div className="flex h-full flex-col bg-sidebar text-sidebar-foreground">
      <BrandRow collapsed={collapsed} />
      {/* Scrolls when nav items overflow short viewports, but without a
          visible scrollbar — the track/thumb read as clutter on the
          rail (same hidden-scrollbar pattern as the document tab bar).
          A flex column so the Settings group can sit at the bottom. */}
      <nav
        aria-label={t('a11y.primary_nav')}
        className="flex flex-1 flex-col gap-2 overflow-y-auto px-2 pb-4 pt-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        {visibleGroups.map((group, i) => (
          <NavGroupBlock
            key={group.labelKey}
            group={group}
            collapsed={collapsed}
            pathname={pathname}
            isFirst={i === 0}
            pinToBottom={group.labelKey === 'sidebar.settings'}
          >
            {group.labelKey === 'sidebar.workspace' && <SmartFoldersBlock collapsed={collapsed} />}
          </NavGroupBlock>
        ))}
      </nav>
      {footer && (
        <div data-testid="sidebar-footer" className="shrink-0 border-t border-sidebar-border px-2 py-2">
          {footer}
        </div>
      )}
    </div>
  )
}

// Desktop sidebar: fixed-position, persistent, collapsible (the header
// toggles it). 240px expanded / 56px collapsed; shown from the `shell`
// breakpoint (801px) up — below it the MobileSidebar sheet takes over.
// app-layout.tsx reserves the matching start padding.
export function AppSidebar() {
  const collapsed = useUIStore((s) => s.sidebarCollapsed)
  return (
    <aside
      data-testid="app-sidebar"
      className={cn(
        'fixed inset-y-0 start-0 z-30 hidden border-e border-sidebar-border bg-sidebar transition-[width] duration-200 ease-out motion-reduce:transition-none shell:block',
        collapsed ? 'w-14' : 'w-60',
      )}
    >
      <SidebarContent collapsed={collapsed} />
    </aside>
  )
}

// Mobile drawer: triggered by the topbar's open-navigation button. Uses the
// canonical Sheet which gets focus-trap, swipe-dismiss, and slide-in
// animations from the canonical Radix surface. The drawer side is
// derived from direction — in RTL the sidebar is the start (=right)
// edge, so the drawer slides in from the right. The shadcn Sheet
// `side` prop is physical; we flip it ourselves rather than vendoring
// a logical-aware fork. Below the shell breakpoint the header has no room
// for the language control, so the sheet carries it in a footer row: on a
// phone this is the only place to switch language.
export function MobileSidebar({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const { t } = useTranslation('common')
  const dir = useDirection()
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side={dir === 'rtl' ? 'right' : 'left'}
        className="w-[280px] border-e border-sidebar-border bg-sidebar p-0 shell:hidden"
      >
        <SheetTitle className="sr-only">{t('a11y.navigation')}</SheetTitle>
        <SidebarContent collapsed={false} footer={<LanguageSelector />} />
      </SheetContent>
    </Sheet>
  )
}
