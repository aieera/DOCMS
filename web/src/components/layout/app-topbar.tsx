import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAppMutation } from '@/hooks/useAppMutation'
import { useTranslation } from 'react-i18next'
import { Bell, CheckCheck, CheckSquare, LogOut, PanelLeft, Search, UserCog } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { useLogout } from '@/hooks/useAuth'
import { listMyTasks, taskKeys } from '@/api/tasks'
import { getNotifications, getUnreadCount, markAllRead, markAsRead } from '@/api/notifications'
import type { Notification } from '@/types/api'
import { createSavedSearch, deleteSavedSearch, listSavedSearches } from '@/api/savedSearches'
import { getRecentSearches, recordRecentSearch, removeRecentSearch } from '@/lib/recentSearches'
import { SearchDropdown } from '@/components/search/SearchDropdown'
import { Breadcrumbs } from './breadcrumbs'
import { ThemeToggle } from './theme-toggle'
import { NotificationsPanel } from '@/components/notifications/NotificationsPanel'
import { LanguageSelector } from '@/components/shared/LanguageSelector'
import { DirectionalIcon } from '@/components/shared/DirectionalIcon'
import { formatRelativeTime } from '@/lib/formatters'
import { shortcutLabel } from '@/lib/platform'
import { cn } from '@/lib/cn'
import { Badge } from '@/components/ui/shadcn/badge'
import { Button } from '@/components/ui/shadcn/button'
import { Kbd } from '@/components/ui/shadcn/kbd'
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
  const { t } = useTranslation('common')
  const collapsed = useUIStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useUIStore((s) => s.toggleSidebar)
  return (
    <header
      role="banner"
      aria-label={t('a11y.app_toolbar')}
      // Flat 56px bar on the page background — a hairline bottom edge
      // separates it from the content. Side padding matches the content
      // column's (16px, 24px from the shell breakpoint).
      className="sticky top-0 z-40 flex h-14 shrink-0 items-center gap-3 border-b border-border bg-background px-4 text-foreground shell:px-6"
    >
      {/* The rail's collapse control lives here, not in the rail: a persistent
          toggle from the shell breakpoint up, the drawer opener below it.
          The rail sits at the inline start, so the glyph mirrors in RTL. */}
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={toggleSidebar}
        className="hidden shell:inline-flex"
        aria-label={collapsed ? t('a11y.expand_sidebar') : t('a11y.collapse_sidebar')}
      >
        <PanelLeft className="h-4 w-4 rtl:-scale-x-100" />
      </Button>
      <Button
        variant="ghost"
        size="icon-sm"
        onClick={onOpenMobileNav}
        className="shell:hidden"
        aria-label={t('a11y.open_navigation')}
      >
        <PanelLeft className="h-4 w-4 rtl:-scale-x-100" />
      </Button>
      <Separator orientation="vertical" className="hidden h-4 shell:block" />

      <div className="hidden min-w-0 flex-1 shell:block">
        <Breadcrumbs />
      </div>
      <div className="flex-1 shell:hidden" />

      <div className="flex items-center gap-2">
        <CommandTrigger />
        <MyTasksBadge />
        <NotificationsDropdown />
        <div className="hidden shell:block">
          <LanguageSelector />
        </div>
        <ThemeToggle />
        <Separator orientation="vertical" className="h-4" />
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
      toast.success(t('saved_search.saved'))
    },
    onError: () => toast.error(t('saved_search.save_error')),
  })
  const unsaveMut = useAppMutation({
    mutationFn: (id: string) => deleteSavedSearch(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['saved-searches'] }),
    onError: () => toast.error(t('saved_search.unsave_error')),
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
  // The search is display:none below the shell breakpoint (801px): there
  // focus() does nothing, so the key is left alone (no preventDefault)
  // rather than swallowed with no visible effect.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const input = inputRef.current
      const isModK = (e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k'
      const target = e.target as HTMLElement | null
      const editingElsewhere =
        target?.tagName === 'INPUT' ||
        target?.tagName === 'TEXTAREA' ||
        target?.isContentEditable ||
        !!target?.closest('[role="combobox"], [role="listbox"], [role="dialog"], [contenteditable], .cm-editor')
      const isSlash = e.key === '/' && !editingElsewhere
      if ((isModK || isSlash) && input && input.getClientRects().length > 0) {
        e.preventDefault()
        input.focus()
        input.select()
      }
      if (e.key === 'Escape' && document.activeElement === input) {
        input?.blur()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="relative hidden shell:flex">
      <form
        role="search"
        onSubmit={(e) => {
          e.preventDefault()
          const trimmed = q.trim()
          if (!trimmed) return
          runQuery(trimmed)
        }}
        // The outline trigger: 280x36, hairline border on the outline
        // surface, 1px ring + shared halo on focus (same as Button).
        className={cn(
          'group flex h-9 w-[280px] items-center gap-2 rounded-md border border-border bg-outline-bg px-3 shadow-xs',
          'transition-shadow focus-within:ring-1 focus-within:ring-ring focus-within:shadow-focus-halo',
        )}
      >
        <Search className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden />
        <input
          ref={inputRef}
          type="search"
          // role="combobox" is what makes aria-expanded/aria-controls
          // valid here — a plain type="search" input's implicit role
          // (searchbox) doesn't support either (axe: aria-allowed-attr).
          // input[type=search] explicitly permits the combobox role
          // override per the ARIA-in-HTML spec.
          role="combobox"
          aria-haspopup="dialog"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onFocus={openDropdown}
          onBlur={() => setOpen(false)}
          placeholder={t('header.search_documents')}
          aria-label={t('header.search_documents')}
          aria-expanded={open}
          aria-controls={open ? 'topbar-search-panel' : undefined}
          className="min-w-0 flex-1 bg-transparent text-sm text-foreground outline-none placeholder:text-muted-foreground"
          autoComplete="off"
          spellCheck={false}
        />
        {/* dir=ltr: the chord re-ordered under RTL (QA SD-07) — a keyboard
            shortcut is always LTR. Hidden while the input has focus. */}
        <Kbd
          aria-hidden="true"
          dir="ltr"
          className="pointer-events-none select-none group-focus-within:invisible"
        >
          {shortcutLabel('K')}
        </Kbd>
      </form>

      {/* Recent + saved searches under the input. mousedown-preventDefault
          keeps the input focused so the panel's click handlers run before
          the blur-close. */}
      {open && (
        <div
          id="topbar-search-panel"
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
  const { t } = useTranslation('common')
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
      // One phrasing for every count rather than an English "s" suffix:
      // Arabic pluralisation has six categories and does not follow the
      // singular/plural split this ternary assumed.
      aria-label={count > 0 ? t('tasks_badge.with_count', { count }) : t('tasks_badge.none')}
      title={count > 0 ? t('tasks_badge.with_count', { count }) : t('tasks_badge.none')}
      data-testid="my-tasks-badge"
    >
      <CheckSquare className="h-4 w-4" />
      {count > 0 && (
        <Badge
          variant="count"
          className="pointer-events-none absolute end-0 top-0"
          data-testid="my-tasks-count"
        >
          {count > 99 ? '99+' : count}
        </Badge>
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
  const navigate = useNavigate()
  const { t } = useTranslation('common')
  // Open state lives in the UI store so the menu can be opened from outside
  // the header (the dashboard's Unread card, the attention list).
  const open = useUIStore((s) => s.notificationsOpen)
  const openNotifications = useUIStore((s) => s.openNotifications)
  const closeNotifications = useUIStore((s) => s.closeNotifications)
  const setOpen = (next: boolean) => (next ? openNotifications() : closeNotifications())
  // The flag outlives this component, so a menu left open when the shell
  // unmounts (logout, a route outside the layout) must not reopen on the
  // next mount.
  useEffect(() => () => closeNotifications(), [closeNotifications])
  const [panelOpen, setPanelOpen] = useState(false)

  // Row activation — see notifications.tsx's openNotification for the
  // same routing rationale (payload carries only the resource's own id,
  // never a workspace id, so a document notification routes through
  // search instead of the detail route).
  const openNotification = (n: Notification) => {
    setOpen(false)
    if (n.resource_type === 'document' && n.resource_id) {
      navigate({ to: '/search', search: { q: n.resource_id } })
      return
    }
    if (n.resource_type === 'task' && n.resource_id) {
      navigate({ to: '/tasks' })
      return
    }
    navigate({ to: '/notifications' })
  }

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
    onError: () => toast.error(t('notifications_menu.error_mark_read')),
  })

  const readAll = useAppMutation({
    mutationFn: markAllRead,
    onSuccess: () => {
      toast.success(t('notifications_menu.marked_all_read'))
      qc.invalidateQueries({ queryKey: ['notifications-inbox'] })
      qc.invalidateQueries({ queryKey: ['notifications', 'unread-count'] })
    },
    onError: () => toast.error(t('notifications_menu.error_mark_all')),
  })

  return (
    <>
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative"
          aria-label={unreadCount > 0
            ? t('notifications_menu.with_count', { count: unreadCount })
            : t('notifications_menu.title')}
          data-testid="notifications-button"
        >
          <Bell className="h-4 w-4" />
          {/* Count, not a bare dot: the tasks badge next to this one
              shows a number, so a dot here made two identical-looking
              affordances behave differently — and the actual unread
              total existed only in the aria-label. */}
          {unreadCount > 0 && (
            <Badge
              variant="count"
              className="pointer-events-none absolute end-0 top-0"
              aria-hidden
              data-testid="notifications-unread-dot"
            >
              {unreadCount > 99 ? '99+' : unreadCount}
            </Badge>
          )}
        </Button>
      </DropdownMenuTrigger>

      <DropdownMenuContent
        align="end"
        sideOffset={8}
        className="flex w-80 flex-col overflow-hidden rounded-lg p-0"
      >
        <div className="flex items-center justify-between border-b border-border px-3 py-2">
          <span className="text-sm font-semibold">{t('notifications_menu.title')}</span>
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
                {t('notifications_menu.mark_all_read')}
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
              {t('notifications_menu.see_all')}
            </button>
          </div>
        </div>

        <div className="max-h-[420px] overflow-y-auto">
          {isLoading ? (
            <div className="flex items-center justify-center py-10 text-xs text-muted-foreground">
              {t('loading')}
            </div>
          ) : items.length === 0 ? (
            <div className="flex flex-col items-center gap-2 py-10 text-center">
              <Bell className="h-8 w-8 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">{t('notifications_menu.empty')}</p>
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
                  {/* Activator — mark-read stays a sibling below, not
                      nested inside, so keyboard/AT don't see an
                      interactive control inside another one. */}
                  <button
                    type="button"
                    onClick={() => openNotification(n)}
                    className="flex min-w-0 flex-1 items-start gap-2 rounded-md text-start focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background"
                    data-testid={`notif-open-${n.id}`}
                  >
                    <span className="mt-1.5 shrink-0">
                      {!n.read ? (
                        <span className="block h-2 w-2 rounded-full bg-destructive" aria-label={t('notifications_menu.unread')} />
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
                  </button>
                  {!n.read && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="h-9 w-9 shrink-0 p-0"
                      onClick={(e) => { e.stopPropagation(); readOne.mutate(n.id) }}
                      title={t('notifications_menu.mark_as_read')}
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
            className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
            onClick={() => setOpen(false)}
          >
            {t('notifications_menu.preferences')}
            {/* The old literal "→" kept pointing right in Arabic, back
                towards the text it was meant to lead away from. */}
            <DirectionalIcon name="ArrowRight" className="h-3 w-3" aria-hidden />
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
          size="icon-sm"
          aria-label={isHydrating ? t('a11y.loading_account') : t('a11y.account_menu')}
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
