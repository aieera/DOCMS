import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useTranslation } from 'react-i18next'
import { Bell, CheckCheck, CheckSquare, LogOut, Menu, Search, UserCog } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks, taskKeys } from '@/api/tasks'
import { getNotifications, getUnreadCount, markAllRead, markAsRead } from '@/api/notifications'
import { createSavedSearch, deleteSavedSearch, listSavedSearches } from '@/api/savedSearches'
import { getRecentSearches, recordRecentSearch, removeRecentSearch } from '@/lib/recentSearches'
import { SearchDropdown } from '@/components/search/SearchDropdown'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import { NotificationsPanel } from '@/components/notifications/NotificationsPanel'
import { LanguageSelector } from '@/components/shared/LanguageSelector'
import { formatRelativeTime } from '@/lib/formatters'
import { isMacPlatform } from '@/lib/platform'
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
      // Soft canvas toolbar matching the shell — a hairline bottom edge
      // separates it from the content below instead of a navy fill.
      className="sticky top-0 z-40 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background px-4 text-foreground lg:px-6"
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
          <LanguageSelector />
          <ThemeToggle />
        </div>
        <Separator orientation="vertical" className="hidden h-6 sm:block" />
        <UserMenu />
      </div>
    </header>
  )
}

function CommandTrigger() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { t } = useTranslation('common')
  const [q, setQ] = useState('')
  const [open, setOpen] = useState(false)
  const [recents, setRecents] = useState<string[]>([])
  const inputRef = useRef<HTMLInputElement>(null)

  // Saved searches for the dropdown — fetched lazily on first open.
  const savedQ = useQuery({
    queryKey: ['saved-searches'],
    queryFn: listSavedSearches,
    enabled: open,
    staleTime: 60_000,
  })
  const saveMut = useAppMutation({
    mutationFn: (query: string) => createSavedSearch({ name: query, query }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['saved-searches'] })
      toast.success('Search saved')
    },
    onError: () => toast.error('Could not save search'),
  })
  const unsaveMut = useAppMutation({
    mutationFn: (id: string) => deleteSavedSearch(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
    onError: () => toast.error('Could not remove saved search'),
  })

  const openDropdown = () => {
    setRecents(getRecentSearches())
    setOpen(true)
  }
  const runQuery = (query: string) => {
    recordRecentSearch(query)
    setOpen(false)
    setQ('')
    inputRef.current?.blur()
    navigate({ to: '/search', search: { q: query } })
  }

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

  // Show Ctrl on non-Mac, ⌘ on Mac — shared with the dashboard's
  // "Search documents" quick-action card so the two never disagree.
  const isMac = isMacPlatform()

  return (
    <div className="relative hidden sm:block">
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault()
          const trimmed = q.trim()
          if (!trimmed) return
          runQuery(trimmed)
        }}
        className={
          'group flex h-10 items-center gap-2 rounded-full border border-input bg-muted px-4 shadow-neu-inset ' +
          'transition-shadow focus-within:ring-2 focus-within:ring-ring focus-within:ring-offset-2 focus-within:ring-offset-background ' +
          'sm:w-64 md:w-80'
        }
      >
        <Search className="h-4 w-4 text-muted-foreground" aria-hidden />
        <input
          ref={inputRef}
          type="search"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onFocus={openDropdown}
          onBlur={() => setOpen(false)}
          placeholder={t('search_placeholder') ?? 'Search documents…'}
          aria-label={t('sidebar.search') ?? 'Search'}
          aria-expanded={open}
          className="flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
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

      {/* Recent + saved searches under the input. mousedown-preventDefault
          keeps the input focused so the panel's click handlers run before
          the blur-close. */}
      {open && (
        <div
          className="absolute inset-x-0 top-11 z-50"
          onMouseDown={(e) => e.preventDefault()}
        >
          <SearchDropdown
            recents={recents}
            saved={savedQ.data ?? []}
            onRun={runQuery}
            onSaveRecent={(query) => saveMut.mutate(query)}
            onUnsave={(id) => unsaveMut.mutate(id)}
            onRemoveRecent={(query) => {
              removeRecentSearch(query)
              setRecents(getRecentSearches())
            }}
            onManage={() => {
              setOpen(false)
              inputRef.current?.blur()
              navigate({ to: '/saved-searches' })
            }}
          />
        </div>
      )}
    </div>
  )
}

function MyTasksBadge() {
  const navigate = useNavigate()
  // Shares the taskKeys.mine() cache with the dashboard KPI/Open-tasks
  // cards (same listMyTasks(false) query) so navigating to the
  // dashboard doesn't fire a second identical request.
  const { data } = useQuery({
    queryKey: taskKeys.mine(),
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
          className="pointer-events-none absolute end-1 top-1 inline-flex h-4 min-w-[1rem] items-center justify-center rounded-full bg-primary px-1 text-[10px] font-bold text-primary-foreground"
          data-testid="my-tasks-count"
        >
          {count > 99 ? '99+' : count}
        </span>
      )}
    </Button>
  )
}

// Bell + dropdown panel. Replaces the prior NotificationsButton that
// navigated to /notifications on click. "See all" opens the
// NotificationsPanel — an end-side slide-in Sheet — rather than
// navigating away; the /notifications route stays deep-linkable but
// is no longer a click target here.
function NotificationsDropdown() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [panelOpen, setPanelOpen] = useState(false)

  // Unread count polls every 30s — drives the red dot on the bell.
  // Cheap endpoint (single SQL COUNT) so polling is fine.
  const { data: unreadCount = 0 } = useQuery({
    queryKey: ['notifications', 'unread-count'],
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

  const readOne = useAppMutation({
    mutationFn: markAsRead,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
      qc.invalidateQueries({ queryKey: ['notifications', 'unread-count'] })
    },
    onError: () => toast.error("Couldn't mark as read"),
  })

  const readAll = useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success('Marked all read')
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
      qc.invalidateQueries({ queryKey: ['notifications', 'unread-count'] })
    },
    onError: () => toast.error("Couldn't mark all as read"),
  })

  return (
    <>
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
          {/* Count, not a bare dot: the tasks badge next to this one
              shows a number, so a dot here made two identical-looking
              affordances behave differently — and the actual unread
              total existed only in the aria-label. */}
          {unreadCount > 0 && (
            <span
              className="pointer-events-none absolute -end-0.5 -top-0.5 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold leading-none text-primary-foreground"
              aria-hidden
              data-testid="notifications-unread-dot"
            >
              {unreadCount > 99 ? '99+' : unreadCount}
            </span>
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
            <button
              type="button"
              className="rounded px-2 py-1 text-xs text-primary hover:underline"
              onClick={() => {
                setOpen(false)
                setPanelOpen(true)
              }}
              data-testid="notif-see-all"
            >
              See all
            </button>
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

    <NotificationsPanel open={panelOpen} onOpenChange={setPanelOpen} />
    </>
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
