import { create } from 'zustand'
import type { User } from '@/types/api'
// ADR 0106 — when the session is hydrated or refreshed, push the
// server-persisted locale into i18next. Importing the singleton (not
// initI18n) so we don't trigger init twice; main.tsx owns that.
import i18n, { applyDocumentDir, SUPPORTED_LOCALES, type SupportedLocale } from '@/i18n'

// syncLocaleFromUser — best-effort. If i18n hasn't initialised yet,
// changeLanguage queues the request and applies it once init resolves.
function syncLocaleFromUser(user: User | null) {
  if (!user?.locale) return
  if (!SUPPORTED_LOCALES.includes(user.locale as SupportedLocale)) return
  if (i18n.language !== user.locale) {
    void i18n.changeLanguage(user.locale)
  }
  applyDocumentDir(user.locale)
}

// NOTE: No persist middleware. The session token lives in an HttpOnly
// cookie set by the auth service on login — the browser sends it
// automatically on every request (axios configured with withCredentials).
// Auth state is rehydrated on app mount by calling GET /api/v1/auth/me;
// until then isAuthenticated is false.
//
// hydrationPromise exists so the axios request interceptor can await an
// in-flight /auth/me before firing follow-up requests during a page
// reload. Without it, components that queue queries before the router's
// beforeLoad resolves go out without X-Auth-Tenant-ID / X-User-ID and
// hit backend handlers that read identity directly from request headers.
interface AuthState {
  user: User | null
  tenantId: string | null
  isAuthenticated: boolean
  hydrationPromise: Promise<void> | null
  login: (user: User, tenantId: string) => void
  logout: () => void
  updateUser: (user: Partial<User>) => void
  setHydration: (p: Promise<void> | null) => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenantId: null,
  isAuthenticated: false,
  hydrationPromise: null,
  login: (user, tenantId) => {
    syncLocaleFromUser(user)
    set({ user, tenantId, isAuthenticated: true, hydrationPromise: null })
  },
  logout: () =>
    set({ user: null, tenantId: null, isAuthenticated: false, hydrationPromise: null }),
  updateUser: (partial) =>
    set((s) => {
      const next = s.user ? { ...s.user, ...partial } : null
      // Only resync i18n if the locale field itself changed —
      // LanguageSelector calls updateUser({ locale }) immediately on
      // change, but most callers update other fields and we don't
      // want every display-name edit to thrash i18n.
      if (partial.locale && next) syncLocaleFromUser(next)
      return { user: next }
    }),
  setHydration: (p) => set({ hydrationPromise: p }),
}))
