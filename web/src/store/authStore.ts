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
// L-2: isHydrating distinguishes "we haven't checked yet" from
// "we checked and the user is anonymous". Components outside the
// routed tree (top-bar avatar, language selector) used to render
// their unauthenticated fallback ("?" initial, default language) for
// a flash before /auth/me resolved on a hard reload. They can now
// gate on isHydrating to render a placeholder until the auth state
// is definitively known.
//
// State transitions:
//   - init                       → isHydrating: true
//   - login()                    → isHydrating: false  (we know who they are)
//   - logout()                   → isHydrating: false  (we know they're anonymous)
//   - markHydrated()             → isHydrating: false  (explicit settle, used
//                                  by ensureHydrated's no-cookie early-return
//                                  and the .finally() of its network attempt)
interface AuthState {
  user: User | null
  tenantId: string | null
  isAuthenticated: boolean
  isHydrating: boolean
  hydrationPromise: Promise<void> | null
  login: (user: User, tenantId: string) => void
  logout: () => void
  updateUser: (user: Partial<User>) => void
  setHydration: (p: Promise<void> | null) => void
  markHydrated: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenantId: null,
  isAuthenticated: false,
  isHydrating: true,
  hydrationPromise: null,
  login: (user, tenantId) => {
    syncLocaleFromUser(user)
    set({ user, tenantId, isAuthenticated: true, isHydrating: false, hydrationPromise: null })
  },
  logout: () =>
    set({ user: null, tenantId: null, isAuthenticated: false, isHydrating: false, hydrationPromise: null }),
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
  markHydrated: () => set({ isHydrating: false }),
}))
