import { Link, useRouterState } from '@tanstack/react-router'
import {
  LayoutDashboard,
  Search,
  CheckSquare,
  Trash2,
  Settings,
  PanelLeftClose,
  PanelLeft,
  FolderOpen,
  Bell,
  Sparkles,
  Bookmark,
  UserCog,
  Database,
  ChevronsUpDown,
  type LucideIcon,
} from 'lucide-react'
import { cn } from '@/lib/cn'
import { Button } from '@/components/ui/shadcn/button'
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/shadcn/sheet'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'

interface NavItem {
  to: string
  icon: LucideIcon
  label: string
  // exact = false means "/admin" matches /admin/users too
  exact?: boolean
  // Restrict the link to a set of roles. Omitted = visible to all.
  // The backend already 403s admin endpoints for non-admin roles, so
  // this is purely a UX guard — but without it a Member sees an
  // empty Admin shell that looks broken (BUG-B).
  roles?: string[]
}

interface NavGroup {
  label: string
  items: NavItem[]
}

const NAV_GROUPS: NavGroup[] = [
  {
    label: 'Workspace',
    items: [
      { to: '/', icon: LayoutDashboard, label: 'Dashboard', exact: true },
      { to: '/workspaces', icon: FolderOpen, label: 'Workspaces' },
      { to: '/search', icon: Search, label: 'Search' },
      { to: '/ask', icon: Sparkles, label: 'Ask' },
      { to: '/saved-searches', icon: Bookmark, label: 'Saved searches' },
    ],
  },
  {
    label: 'Inbox',
    items: [
      { to: '/tasks', icon: CheckSquare, label: 'Tasks' },
      { to: '/notifications', icon: Bell, label: 'Notifications' },
      { to: '/trash', icon: Trash2, label: 'Trash' },
    ],
  },
  {
    label: 'Settings',
    items: [
      { to: '/settings/security', icon: UserCog, label: 'My settings' },
      { to: '/admin', icon: Settings, label: 'Admin', roles: ['admin', 'owner'] },
    ],
  },
]

function isActive(pathname: string, item: NavItem) {
  if (item.exact) return pathname === item.to
  if (item.to === '/') return pathname === '/'
  return pathname === item.to || pathname.startsWith(item.to + '/')
}

function NavLink({ item, collapsed, pathname }: { item: NavItem; collapsed: boolean; pathname: string }) {
  const active = isActive(pathname, item)
  const Icon = item.icon
  return (
    <Link
      to={item.to}
      title={collapsed ? item.label : undefined}
      aria-label={item.label}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'group relative flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        active
          ? 'bg-sidebar-accent text-sidebar-accent-foreground'
          : 'text-muted-foreground hover:bg-sidebar-accent/60 hover:text-foreground',
        collapsed && 'justify-center px-0',
      )}
    >
      {/* Active indicator rail — subtle accent bar on the leading
          edge so the active row reads at a glance even in collapsed
          mode where the label is hidden. */}
      {active && (
        <span
          aria-hidden
          className="absolute inset-y-1 start-0 w-0.5 rounded-r-full bg-foreground"
        />
      )}
      <Icon className={cn('h-[18px] w-[18px] shrink-0', active && 'text-foreground')} />
      {!collapsed && <span className="truncate">{item.label}</span>}
    </Link>
  )
}

function BrandRow({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  return (
    <div className={cn('flex h-14 items-center border-b border-sidebar-border px-3', collapsed && 'justify-center px-2')}>
      {!collapsed && (
        <Link to="/" className="flex flex-1 items-center gap-2 rounded-md px-2 py-1 transition-colors hover:bg-sidebar-accent/60">
          <span className="flex h-7 w-7 items-center justify-center rounded-md bg-foreground text-background">
            <Database className="h-4 w-4" />
          </span>
          <span className="text-sm font-semibold tracking-tight">VaultDMS</span>
        </Link>
      )}
      <Button
        variant="ghost"
        size="icon"
        onClick={onToggle}
        aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
        className="h-8 w-8 text-muted-foreground hover:bg-sidebar-accent hover:text-foreground"
      >
        {collapsed ? <PanelLeft className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
      </Button>
    </div>
  )
}

function WorkspaceCard({ collapsed }: { collapsed: boolean }) {
  const user = useAuthStore((s) => s.user)
  if (collapsed || !user) return null
  // Tenant slug isn't on user, but display_name fits as tenant
  // identity for now; later this becomes a workspace switcher
  // populated from /auth/me.tenants[].
  return (
    <div className="px-3 pb-2 pt-3">
      <button
        type="button"
        className="flex w-full items-center gap-2 rounded-md border border-sidebar-border bg-background/50 px-2.5 py-2 text-left transition-colors hover:bg-sidebar-accent/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-xs font-semibold text-primary-foreground">
          {user.display_name?.charAt(0)?.toUpperCase() ?? '?'}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-xs font-semibold">{user.display_name ?? 'Account'}</span>
          <span className="block truncate text-[11px] text-muted-foreground">{user.email}</span>
        </span>
        <ChevronsUpDown className="h-3.5 w-3.5 text-muted-foreground" />
      </button>
    </div>
  )
}

function NavGroupBlock({ group, collapsed, pathname }: { group: NavGroup; collapsed: boolean; pathname: string }) {
  return (
    <div className="space-y-0.5">
      {!collapsed && (
        <div className="px-3 pb-1 pt-3 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/70">
          {group.label}
        </div>
      )}
      {group.items.map((item) => (
        <NavLink key={item.to} item={item} collapsed={collapsed} pathname={pathname} />
      ))}
    </div>
  )
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
      <WorkspaceCard collapsed={collapsed} />
      <nav aria-label="Primary" className={cn('flex-1 overflow-y-auto px-2 pb-4', collapsed && 'px-1.5')}>
        {visibleGroups.map((group) => (
          <NavGroupBlock key={group.label} group={group} collapsed={collapsed} pathname={pathname} />
        ))}
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
// canonical Sheet (side="left") so it gets the focus-trap, swipe-
// dismiss, and slide-in animations from the canonical Radix
// surface — and so the strangler can finally retire the inline
// Radix Dialog wiring this component used to do.
export function MobileSidebar({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="left"
        className="w-[280px] border-e border-sidebar-border bg-sidebar p-0 lg:hidden"
      >
        <SheetTitle className="sr-only">Navigation</SheetTitle>
        <SidebarContent collapsed={false} onToggle={() => onOpenChange(false)} />
      </SheetContent>
    </Sheet>
  )
}
