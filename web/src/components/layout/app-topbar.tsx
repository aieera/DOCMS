import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Bell, CheckCheck, CheckSquare, LogOut, Menu, Search, UserCog } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks } from '@/api/tasks'
import { getNotifications, getUnreadCount, markAllRead, markAsRead } from '@/api/notifications'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import { LanguageSelector } from '@/components/shared/LanguageSelector'
import { formatRelativeTime } from '@/lib/formatters'
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
    <header
      role="banner"
      aria-label="Application toolbar"
      className="sticky top-0 z-40 flex h-14 shrink-0 items-center gap-3 border-b border-border/80 bg-background px-4 shadow-sm lg:px-6"
    >
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

      <div className="flex items-center gap-2">
        <CommandTrigger />
        <Separator orientation="vertical" className="hidden h-6 sm:block" />
        <div className="flex items-center gap-2">
          <MyTasksBadge />
          <NotificationsDropdown />
          <ThemeToggle />
          <LanguageSelector />
        </div>
        <Separator orientation="vertical" className="hidden h-6 sm:block" />
        <UserMenu />
      </div>
    </header>
  )
}

function CommandTrigger() {
  const navigate = useNavigate()
  const { t } = useTranslation('common')
  const [q, setQ] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  // Cmd/Ctrl+K (and `/` when nothing else is taking input) focuses the
  // header search input. The `/` path must NOT trigger inside Radix
  // popovers/comboboxes, contenteditable surfaces, or CodeMirror
  // editors used elsewhere — otherwise typing a clause path or query
  // that contains `/` yanks the user out of their current control.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const isModK = (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k'
      const target = e.target as HTMLElement | null
      const editingElsewhere =
        target?.tagName === 'INPUT' ||
        target?.tagName === 'TEXTAREA' ||
        target?.isContentEditable ||
        !!target?.closest('[role="combobox"], [role="listbox"], [role="dialog"], [contenteditable], .cm-editor')
      const isSlash = e.key === '/' && !editingElsewhere
      if (isModK || isSlash) {
        e.preventDefault()
        inputRef.current?.focus()
        inputRef.current?.select()
      }
      if (e.key === 'Escape' && document.activeElement === inputRef.current) {
        inputRef.current?.blur()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // Show Ctrl on non-Mac, ⌘ on Mac. navigator.platform is deprecated
  // but still the most reliable signal here; userAgentData is uneven.
  const isMac = typeof navigator !== 'undefined' && /mac/i.test(navigator.platform)

  return (
    <form
      role="search"
      onSubmit={(e) => {
        e.preventDefault()
        const trimmed = q.trim()
        if (!trimmed) return
        navigate({ to: '/search', search: { q: trimmed } })
      }}
      className={
        'group hidden h-9 items-center gap-2 rounded-md border border-input bg-background px-3 ' +
        'transition-colors focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/40 ' +
        'hover:border-ring/60 sm:inline-flex sm:w-64 md:w-80'
      }
    >
      <Search className="h-4 w-4 text-muted-foreground" aria-hidden />
      <input
        ref={inputRef}
        type="search"
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder={t('search_placeholder') ?? 'Search documents…'}
        aria-label={t('sidebar.search') ?? 'Search'}
        className="flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
        autoComplete="off"
        spellCheck={false}
      />
      <kbd
        aria-hidden
        className={
          'pointer-events-none inline-flex h-5 select-none items-center gap-0.5 rounded border ' +
          'border-border bg-background px-1.5 font-mono text-[11px] font-medium text-foreground/70 ' +
          'group-focus-within:invisible'
        }
      >
        {isMac ? <span className="text-xs">⌘</span> : <span>Ctrl</span>}
        <span>K</span>
      </kbd>
    </form>
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

// Bell + dropdown panel. Replaces the prior NotificationsButton that
// navigated to /notifications on click. The full-page route is still
// reachable via the "See all" link in the dropdown header and the
// /settings/notifications preferences deep link in the footer — both
// still need to exist as a destination, just not as the primary
// affordance.
function NotificationsDropdown() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)

  // Unread count polls every 30s — drives the red dot on the bell.
  // Cheap endpoint (single SQL COUNT) so polling is fine.
  const { data: unreadCount = 0 } = useQuery({
    queryKey: ['notifications-unread-count'],
    queryFn: getUnreadCount,
    refetchInterval: 30_000,
    staleTime: 15_000,
  })

  // Full list is fetched only when the dropdown opens — no point
  // paying for the heavier query on every page when most users won't
  // click the bell. enabled: open is what makes it lazy.
  const { data: listData, isLoading } = useQuery({
    queryKey: ['notifications-inbox'],
    queryFn: () => getNotifications(),
    enabled: open,
    staleTime: 10_000,
  })
  const items = listData?.items ?? []

  const readOne = useMutation({
    mutationFn: markAsRead,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
      qc.invalidateQueries({ queryKey: ['notifications-unread-count'] })
    },
    onError: () => toast.error("Couldn't mark as read"),
  })

  const readAll = useMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
      qc.invalidateQueries({ queryKey: ['notifications-unread-count'] })
    },
    onError: () => toast.error("Couldn't mark all as read"),
  })

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative"
          aria-label={unreadCount > 0 ? `${unreadCount} unread notifications` : 'Notifications'}
          data-testid="notifications-button"
        >
          <Bell className="h-[1.1rem] w-[1.1rem]" />
          {unreadCount > 0 && (
            <span
              className="pointer-events-none absolute end-1 top-1 h-2 w-2 rounded-full bg-destructive"
              aria-hidden
              data-testid="notifications-unread-dot"
            />
          )}
        </Button>
      </DropdownMenuTrigger>

      <DropdownMenuContent
        align="end"
        sideOffset={8}
        className="flex w-80 flex-col overflow-hidden rounded-lg p-0 shadow-lg"
      >
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <span className="text-sm font-semibold">Notifications</span>
          <div className="flex items-center gap-1">
            {items.length > 0 && (
              <Button
                size="sm"
                variant="ghost"
                className="h-7 px-2 text-xs"
                onClick={() => readAll.mutate()}
                disabled={readAll.isPending}
                data-testid="notif-mark-all-read"
              >
                <CheckCheck className="me-1 h-3.5 w-3.5" />
                Mark all read
              </Button>
            )}
            <Link
              to="/notifications"
              className="rounded px-2 py-1 text-xs text-primary hover:underline"
              onClick={() => setOpen(false)}
              data-testid="notif-see-all"
            >
              See all
            </Link>
          </div>
        </div>

        <div className="max-h-[420px] overflow-y-auto">
          {isLoading ? (
            <div className="flex items-center justify-center py-10 text-xs text-muted-foreground">
              Loading…
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <Bell className="h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">You're all caught up</p>
            </div>
          ) : (
            <ul className="divide-y divide-border">
              {items.map((n) => (
                <li
                  key={n.id}
                  className={`flex items-start gap-2 px-3 py-2.5 transition-colors hover:bg-muted/50 ${
                    n.read ? 'opacity-60' : ''
                  }`}
                  data-testid={`notif-row-${n.id}`}
                >
                  <span className="mt-1.5 shrink-0">
                    {!n.read ? (
                      <span className="block h-2 w-2 rounded-full bg-destructive" aria-label="Unread" />
                    ) : (
                      <span className="block h-2 w-2" />
                    )}
                  </span>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium">{n.title}</p>
                    {n.body && (
                      <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">{n.body}</p>
                    )}
                    <p
                      className="mt-0.5 text-[10px] text-muted-foreground"
                      title={new Date(n.created_at).toLocaleString()}
                    >
                      {formatRelativeTime(n.created_at)}
                    </p>
                  </div>
                  {!n.read && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-7 w-7 shrink-0 p-0"
                      onClick={(e) => { e.stopPropagation(); readOne.mutate(n.id) }}
                      title="Mark as read"
                      data-testid={`notif-read-${n.id}`}
                    >
                      <CheckCheck className="h-3.5 w-3.5" />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="border-t border-border px-3 py-2">
          <Link
            to="/settings/notifications"
            className="text-xs text-muted-foreground hover:text-foreground hover:underline"
            onClick={() => setOpen(false)}
          >
            Notification preferences →
          </Link>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
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
          variant="secondary"
          size="icon"
          aria-label={isHydrating ? 'Loading account…' : 'Account menu'}
          data-testid="user-avatar-button"
          className="rounded-full font-semibold"
          disabled={isHydrating}
        >
          {isHydrating ? (
            <span aria-hidden className="inline-block h-3 w-3 animate-pulse rounded-full bg-foreground/20" />
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
