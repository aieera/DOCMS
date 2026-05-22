import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Bell, CheckSquare, LogOut, Menu, Search, UserCog } from 'lucide-react'
import { useAuthStore } from '@/store/authStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks } from '@/api/tasks'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import { LanguageSelector } from '@/components/shared/LanguageSelector'
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
        <LanguageSelector />
        <Separator orientation="vertical" className="mx-1 hidden h-6 sm:block" />
        <UserMenu />
      </div>
    </header>
  )
}

function CommandTrigger() {
  const navigate = useNavigate()
  const { t } = useTranslation('common')
  return (
    <Button
      variant="outline"
      onClick={() => navigate({ to: '/search' })}
      className="hidden h-9 justify-start gap-2 px-3 text-sm font-normal text-muted-foreground hover:text-foreground sm:inline-flex sm:w-64 md:w-80"
      aria-label={t('sidebar.search')}
    >
      <Search className="h-4 w-4" />
      <span className="flex-1 text-start">{t('search_placeholder')}</span>
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
          className="pointer-events-none absolute end-1 top-1 inline-flex h-4 min-w-[1rem] items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-bold text-destructive-foreground"
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
  // L-2: during the hard-reload window between mount and /auth/me
  // resolving, `user` is null and the avatar previously rendered a
  // "?" initial — a flash of "looks logged out" before the real
  // initial appears. Gate on isHydrating so the initial slot stays
  // blank until auth state is definitively known. The dropdown
  // contents still gate on `user`, so opening the menu mid-hydration
  // shows nothing rather than empty rows.
  const isHydrating = useAuthStore((s) => s.isHydrating)
  const navigate = useNavigate()
  const logout = useLogout()
  const { t } = useTranslation('common')
  const initial = user?.display_name?.charAt(0)?.toUpperCase() ?? '?'
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="default"
          size="icon"
          aria-label={isHydrating ? 'Loading account…' : 'Account menu'}
          data-testid="user-avatar-button"
          className="rounded-full font-semibold"
          disabled={isHydrating}
        >
          {isHydrating ? (
            <span aria-hidden className="inline-block h-3 w-3 animate-pulse rounded-full bg-current/30" />
          ) : (
            initial
          )}
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
          {t('user_menu.settings')}
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onSelect={() => logout()} className="text-destructive focus:text-destructive">
          <LogOut className="h-4 w-4" />
          {t('user_menu.log_out')}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
