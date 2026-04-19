import { create } from 'zustand'
import type { User } from '@/types/api'

// NOTE: No persist middleware. The session token lives in an HttpOnly
// cookie set by the auth service on login — the browser sends it
// automatically on every request (axios configured with withCredentials).
// Auth state is rehydrated on app mount by calling GET /api/v1/auth/me;
// until then isAuthenticated is false.
interface AuthState {
  user: User | null
  tenantId: string | null
  isAuthenticated: boolean
  login: (user: User, tenantId: string) => void
  logout: () => void
  updateUser: (user: Partial<User>) => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenantId: null,
  isAuthenticated: false,
  login: (user, tenantId) =>
    set({ user, tenantId, isAuthenticated: true }),
  logout: () =>
    set({ user: null, tenantId: null, isAuthenticated: false }),
  updateUser: (partial) =>
    set((s) => ({ user: s.user ? { ...s.user, ...partial } : null })),
}))
