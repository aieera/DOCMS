import { useUIStore } from '@/store/uiStore'
import { cn } from '@/lib/cn'
import { LayoutDashboard, Search, CheckSquare, Trash2, Settings, PanelLeftClose, PanelLeft, FolderOpen, Bell, Sparkles, Bookmark, UserCog } from 'lucide-react'
import { Link } from '@tanstack/react-router'

const navItems = [
  { to: '/', icon: LayoutDashboard, label: 'Dashboard' },
  { to: '/workspaces', icon: FolderOpen, label: 'Workspaces' },
  { to: '/search', icon: Search, label: 'Search' },
  { to: '/ask', icon: Sparkles, label: 'Ask' },
  { to: '/saved-searches', icon: Bookmark, label: 'Saved searches' },
  { to: '/tasks', icon: CheckSquare, label: 'Tasks' },
  { to: '/notifications', icon: Bell, label: 'Notifications' },
  { to: '/trash', icon: Trash2, label: 'Trash' },
  // /settings = the user's OWN account (passkeys, security, future
  // profile fields). /admin = tenant-wide settings. Two distinct
  // surfaces — keeping them separate so a non-admin user has a
  // place to manage their own credentials.
  { to: '/settings/security', icon: UserCog, label: 'Settings' },
  { to: '/admin', icon: Settings, label: 'Admin' },
] as const

export function Sidebar() {
  const { sidebarCollapsed, toggleSidebar } = useUIStore()
  return (
    <aside className={cn(
      'fixed inset-y-0 start-0 z-30 flex flex-col border-e border-[var(--color-border)] bg-[var(--color-bg-secondary)] transition-all duration-200',
      sidebarCollapsed ? 'w-16' : 'w-[280px]',
    )}>
      <div className="flex h-14 items-center justify-between border-b border-[var(--color-border)] px-4">
        {!sidebarCollapsed && <span className="text-lg font-bold text-[var(--color-primary)]">VaultDMS</span>}
        <button onClick={toggleSidebar} className="rounded-md p-1.5 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Toggle sidebar">
          {sidebarCollapsed ? <PanelLeft className="h-5 w-5" /> : <PanelLeftClose className="h-5 w-5" />}
        </button>
      </div>
      <nav className="flex-1 space-y-1 p-2">
        {navItems.map(({ to, icon: Icon, label }) => (
          <Link key={to} to={to} className={cn(
            'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium text-[var(--color-text-secondary)] transition-colors hover:bg-slate-100 hover:text-[var(--color-text)] dark:hover:bg-slate-800',
            sidebarCollapsed && 'justify-center px-0',
          )}>
            <Icon className="h-5 w-5 shrink-0" />
            {!sidebarCollapsed && <span>{label}</span>}
          </Link>
        ))}
      </nav>
    </aside>
  )
}
