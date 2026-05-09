import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Bell, CheckSquare, LogOut, Menu, Search, UserCog } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks } from '@/api/tasks'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from '@/components/ui/DropdownMenu'

interface AppTopbarProps {
  onOpenMobileNav: () => void
}

export function AppTopbar({ onOpenMobileNav }: AppTopbarProps) {
  return (
    <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/80 lg:px-6">
      <button
        type="button"
        onClick={onOpenMobileNav}
        className="inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring lg:hidden"
        aria-label="Open navigation"
      >
        <Menu className="h-5 w-5" />
      </button>

      <div className="hidden min-w-0 flex-1 lg:block">
        <Breadcrumbs />
      </div>
      {/* On mobile we hide the breadcrumbs to keep the bar uncluttered;
          the page heading carries the same context one row down. */}
      <div className="flex-1 lg:hidden" />

      <div className="flex items-center gap-1.5">
        <CommandTrigger />
        <MyTasksBadge />
        <NotificationsButton />
        <ThemeToggle />
        <div className="mx-1 hidden h-6 w-px bg-border sm:block" aria-hidden />
        <UserMenu />
      </div>
    </header>
  )
}

function CommandTrigger() {
  const navigate = useNavigate()
  return (
    <button
      type="button"
      onClick={() => navigate({ to: '/search' })}
      className="hidden h-9 items-center gap-2 rounded-md border border-input bg-background px-3 text-sm text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:inline-flex sm:w-64 md:w-80"
      aria-label="Open search"
    >
      <Search className="h-4 w-4" />
      <span className="flex-1 text-left">Search documents…</span>
      <kbd className="pointer-events-none inline-flex h-5 select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[10px] font-medium text-muted-foreground">
        <span className="text-xs">⌘</span>K
      </kbd>
    </button>
  )
}

function MyTasksBadge() {
  const navigate = useNavigate()
  const { data } = useQuery({
    queryKey: ['my-tasks-count'],
    queryFn: () => listMyTasks(false),
    refetchInterval: 30_000,
    staleTime: 15_000,
  })
  const count = (data ?? []).filter((t) => t.status === 'open' || t.status === 'in_progress').length
  return (
    <button
      type="button"
      onClick={() => navigate({ to: '/tasks' })}
      className="relative inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      aria-label={count > 0 ? `${count} open tasks` : 'My tasks'}
      title={count > 0 ? `${count} open task${count === 1 ? '' : 's'}` : 'My tasks'}
      data-testid="my-tasks-badge"
    >
      <CheckSquare className="h-[1.1rem] w-[1.1rem]" />
      {count > 0 && (
        <span
          className="pointer-events-none absolute right-1 top-1 inline-flex h-4 min-w-[1rem] items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-bold text-destructive-foreground"
          data-testid="my-tasks-count"
        >
          {count > 99 ? '99+' : count}
        </span>
      )}
    </button>
  )
}

function NotificationsButton() {
  const navigate = useNavigate()
  return (
    <button
      type="button"
      onClick={() => navigate({ to: '/notifications' })}
      className="relative inline-flex h-9 w-9 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      aria-label="Notifications"
    >
      <Bell className="h-[1.1rem] w-[1.1rem]" />
    </button>
  )
}

function UserMenu() {
  const user = useAuthStore((s) => s.user)
  const navigate = useNavigate()
  const logout = useLogout()
  const initial = user?.display_name?.charAt(0)?.toUpperCase() ?? '?'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          aria-label="Account menu"
          data-testid="user-avatar-button"
          className="flex h-9 w-9 items-center justify-center rounded-full bg-primary text-xs font-semibold text-primary-foreground transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        >
          {initial}
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[220px]">
        {user && (
          <div className="border-b border-border px-2 pb-2 pt-1">
            <p className="truncate text-sm font-medium">{user.display_name}</p>
            <p className="truncate text-xs text-muted-foreground">{user.email}</p>
          </div>
        )}
        <div className="pt-1" />
        <DropdownMenuItem icon={<UserCog className="h-4 w-4" />} onSelect={() => navigate({ to: '/settings/security' })}>
          Settings
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem icon={<LogOut className="h-4 w-4" />} destructive onSelect={() => logout()}>
          Log out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
