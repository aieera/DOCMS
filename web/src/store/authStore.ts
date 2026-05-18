import { create } from 'zustand'
import type { User } from '@/types/api'

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
  login: (user, tenantId) =>
    set({ user, tenantId, isAuthenticated: true, hydrationPromise: null }),
  logout: () =>
    set({ user: null, tenantId: null, isAuthenticated: false, hydrationPromise: null }),
  updateUser: (partial) =>
    set((s) => ({ user: s.user ? { ...s.user, ...partial } : null })),
  setHydration: (p) => set({ hydrationPromise: p }),
}))
