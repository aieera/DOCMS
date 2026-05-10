import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Bell, CheckSquare, LogOut, Menu, Search, UserCog } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks } from '@/api/tasks'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import { Button } from '@/components/ui/shadcn/button'
import { Separator } from '@/components/ui/shadcn/separator'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/shadcn/dropdown-menu'

interface AppTopbarProps {
  onOpenMobileNav: () => void
}

export function AppTopbar({ onOpenMobileNav }: AppTopbarProps) {
  return (
    <header className="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background/95 px-4 backdrop-blur supports-[backdrop-filter]:bg-background/80 lg:px-6">
      <Button
        variant="ghost"
        size="icon"
        onClick={onOpenMobileNav}
        className="lg:hidden"
        aria-label="Open navigation"
      >
        <Menu className="h-5 w-5" />
      </Button>

      <div className="hidden min-w-0 flex-1 lg:block">
        <Breadcrumbs />
      </div>
      <div className="flex-1 lg:hidden" />

      <div className="flex items-center gap-1.5">
        <CommandTrigger />
        <MyTasksBadge />
        <NotificationsButton />
        <ThemeToggle />
        <Separator orientation="vertical" className="mx-1 hidden h-6 sm:block" />
        <UserMenu />
      </div>
    </header>
  )
}

function CommandTrigger() {
  const navigate = useNavigate()
  return (
    <Button
      variant="outline"
      onClick={() => navigate({ to: '/search' })}
      className="hidden h-9 justify-start gap-2 px-3 text-sm font-normal text-muted-foreground hover:text-foreground sm:inline-flex sm:w-64 md:w-80"
      aria-label="Open search"
    >
      <Search className="h-4 w-4" />
      <span className="flex-1 text-left">Search documents…</span>
      <kbd className="pointer-events-none inline-flex h-5 select-none items-center gap-0.5 rounded border border-border bg-muted px-1.5 font-mono text-[10px] font-medium text-muted-foreground">
        <span className="text-xs">⌘</span>K
      </kbd>
    </Button>
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
    <Button
      variant="ghost"
      size="icon"
      onClick={() => navigate({ to: '/tasks' })}
      className="relative"
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
    </Button>
  )
}

function NotificationsButton() {
  const navigate = useNavigate()
  return (
    <Button
      variant="ghost"
      size="icon"
      onClick={() => navigate({ to: '/notifications' })}
      aria-label="Notifications"
    >
      <Bell className="h-[1.1rem] w-[1.1rem]" />
    </Button>
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
        <Button
          variant="default"
          size="icon"
          aria-label="Account menu"
          data-testid="user-avatar-button"
          className="rounded-full font-semibold"
        >
          {initial}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-[220px]">
        {user && (
          <>
            <DropdownMenuLabel className="font-normal">
              <div className="flex flex-col space-y-0.5">
                <p className="truncate text-sm font-medium leading-none">{user.display_name}</p>
                <p className="truncate text-xs leading-none text-muted-foreground">{user.email}</p>
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem onSelect={() => navigate({ to: '/settings/security' })}>
          <UserCog className="h-4 w-4" />
          Settings
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => logout()} className="text-destructive focus:text-destructive">
          <LogOut className="h-4 w-4" />
          Log out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
