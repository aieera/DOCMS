import { Link, useRouterState } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useDirection } from '@/hooks/useDirection'
import { LayoutDashboard, CheckSquare, Trash2, Settings, PanelLeftClose, PanelLeft, FolderOpen, Boxes, Sparkles, BookOpen, UserCog, Database, Lock, Users, Globe, Inbox, type LucideIcon , BarChart3 } from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/shadcn/sheet'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { listSmartFolders, type SavedSearch, type TreeVisibility } from '@/api/savedSearches'

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
      className={cn(
        'group relative flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition-[box-shadow,color] duration-150',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        active
          ? 'bg-background text-primary shadow-neu-inset'
          : 'text-sidebar-foreground hover:text-primary hover:shadow-neu-sm',
        collapsed && 'justify-center px-0',
      )}
    >
      {/* Active indicator rail — accent leading-edge so the active row
          reads at a glance against the pressed-in inset background. */}
      {active && (
        <span
          aria-hidden
          className="absolute inset-y-1.5 start-0 w-[3px] rounded-e-full bg-primary"
        />
      )}
      <Icon className={cn('h-[18px] w-[18px] shrink-0', active ? 'text-primary' : 'text-sidebar-foreground/70 group-hover:text-primary')} />
      {!collapsed && <span className="truncate">{label}</span>}
    </Link>
  )
}

function BrandRow({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  return (
    <div className={cn('flex h-14 items-center border-b border-sidebar-border px-3', collapsed && 'justify-center px-2')}>
      {!collapsed && (
        <Link to="/" className="flex flex-1 items-center gap-2.5 rounded-lg px-2 py-1 transition-shadow hover:shadow-neu-sm">
          <span className="flex h-8 w-8 items-center justify-center rounded-xl bg-primary text-primary-foreground shadow-neu-sm">
            <Database className="h-[18px] w-[18px]" />
          </span>
          <span className="text-[15px] font-semibold tracking-tight text-sidebar-foreground">SeDoc</span>
        </Link>
      )}
      <Button
        variant="ghost"
        size="icon"
        onClick={onToggle}
        aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        className="h-9 w-9 text-sidebar-foreground"
      >
        {collapsed ? <PanelLeft className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
      </Button>
    </div>
  )
}

function NavGroupBlock({ group, collapsed, pathname, isFirst }: { group: NavGroup; collapsed: boolean; pathname: string; isFirst: boolean }) {
  const { t } = useTranslation('common')
  return (
    <div className={cn('space-y-0.5', !isFirst && !collapsed && 'mt-3 border-t border-sidebar-border pt-3')}>
      {!collapsed && (
        <div className="px-3 pb-1.5 pt-2 text-[11px] font-semibold uppercase tracking-[0.08em] text-sidebar-foreground/70">
          {t(group.labelKey)}
        </div>
      )}
      {group.items.map((item) => (
        <NavLink key={item.to} item={item} collapsed={collapsed} pathname={pathname} />
      ))}
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
      <div className="flex items-center gap-1 px-3 pb-1 pt-3 text-[11px] font-semibold uppercase tracking-wider text-sidebar-foreground/70">
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
      className="group relative mx-2 flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm text-sidebar-foreground/70 transition-[box-shadow,color] duration-150 hover:text-primary hover:shadow-neu-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid={`smart-folder-${sf.id}`}
    >
      <Sparkles className="h-[18px] w-[18px] shrink-0 text-violet-300" />
      <span className="truncate">{sf.name}</span>
      <Vis className="ms-auto h-3 w-3 opacity-60" aria-label={sf.tree_visibility ?? 'private'} />
    </Link>
  )
}

function visibilityIcon(v?: TreeVisibility) {
  if (v === 'workspace') return Users
  if (v === 'public') return Globe
  return Lock
}

export function SidebarContent({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
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
      <BrandRow collapsed={collapsed} onToggle={onToggle} />
      {/* Scrolls when nav items overflow short viewports, but without a
          visible scrollbar — the track/thumb read as clutter on the
          rail (same hidden-scrollbar pattern as the document tab bar). */}
      <nav
        aria-label="Primary"
        className={cn(
          'flex-1 overflow-y-auto px-2 pb-4 pt-3 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
          collapsed && 'px-1.5',
        )}
      >
        {visibleGroups.map((group, i) => (
          <NavGroupBlock key={group.labelKey} group={group} collapsed={collapsed} pathname={pathname} isFirst={i === 0} />
        ))}
        <SmartFoldersBlock collapsed={collapsed} />
      </nav>
    </div>
  )
}

// Desktop sidebar: fixed-position, persistent, collapsible.
export function AppSidebar() {
  const collapsed = useUIStore((s) => s.sidebarCollapsed)
  const toggle = useUIStore((s) => s.toggleSidebar)
  return (
    <aside
      className={cn(
        'fixed inset-y-0 start-0 z-30 hidden border-e border-sidebar-border transition-[width] duration-200 ease-out lg:block',
        collapsed ? 'w-16' : 'w-[260px]',
      )}
    >
      <SidebarContent collapsed={collapsed} onToggle={toggle} />
    </aside>
  )
}

// Mobile drawer: triggered by the topbar's menu button. Uses the
// canonical Sheet which gets focus-trap, swipe-dismiss, and slide-in
// animations from the canonical Radix surface. The drawer side is
// derived from direction — in RTL the sidebar is the start (=right)
// edge, so the drawer slides in from the right. The shadcn Sheet
// `side` prop is physical; we flip it ourselves rather than vendoring
// a logical-aware fork.
export function MobileSidebar({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const dir = useDirection()
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side={dir === 'rtl' ? 'right' : 'left'}
        className="w-[280px] border-e border-sidebar-border bg-sidebar p-0 lg:hidden"
      >
        <SheetTitle className="sr-only">Navigation</SheetTitle>
        <SidebarContent collapsed={false} onToggle={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}
