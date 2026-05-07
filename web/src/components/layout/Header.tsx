import { useAuthStore } from '@/store/authStore'
import { useUIStore } from '@/store/uiStore'
import { useLogout } from '@/hooks/useAuth'
import { Bell, Moon, Sun, LogOut, Search } from 'lucide-react'
import { useNavigate } from '@tanstack/react-router'

export function Header() {
  const user = useAuthStore((s) => s.user)
  // useLogout POSTs /api/v1/auth/logout to invalidate the session
  // cookie server-side, clears the local store, then navigates to
  // /login. Calling authStore.logout() directly skips the first two
  // and leaves the user stranded on the same page.
  const logout = useLogout()
  const { theme, toggleTheme } = useUIStore()
  const navigate = useNavigate()

  return (
    <header className="flex h-14 shrink-0 items-center justify-between border-b border-[var(--color-border)] bg-[var(--color-bg-secondary)] px-6">
      <button
        onClick={() => navigate({ to: '/search' })}
        className="flex h-9 w-80 items-center gap-2 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 text-sm text-[var(--color-text-secondary)] transition-colors hover:border-[var(--color-primary)]"
      >
        <Search className="h-4 w-4" />
        <span>Search documents...</span>
        <kbd className="ms-auto rounded bg-slate-100 px-1.5 py-0.5 text-xs dark:bg-slate-700">⌘K</kbd>
      </button>

      <div className="flex items-center gap-2">
        <button onClick={() => navigate({ to: '/notifications' })} className="relative rounded-md p-2 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Notifications">
          <Bell className="h-5 w-5" />
        </button>
        <button onClick={toggleTheme} className="rounded-md p-2 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Toggle theme">
          {theme === 'light' ? <Moon className="h-5 w-5" /> : <Sun className="h-5 w-5" />}
        </button>
        <div className="ms-2 flex items-center gap-2">
          <button
            onClick={() => navigate({ to: '/settings/security' })}
            className="flex h-8 w-8 items-center justify-center rounded-full bg-[var(--color-primary)] text-xs font-medium text-white hover:opacity-80"
            aria-label="Security settings"
            title="Security settings"
            data-testid="user-avatar-button"
          >
            {user?.display_name?.charAt(0)?.toUpperCase() || '?'}
          </button>
          <button onClick={logout} className="rounded-md p-2 hover:bg-slate-100 dark:hover:bg-slate-800" aria-label="Logout">
            <LogOut className="h-4 w-4" />
          </button>
        </div>
      </div>
    </header>
  )
}
