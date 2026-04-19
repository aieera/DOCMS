import { create } from 'zustand'

interface User { id: string; email: string; display_name: string; role: string; tenant_id: string }

interface AuthState {
  user: User | null
  token: string | null
  tenantId: string | null
  isAuthenticated: boolean
  login: (user: User, token: string, tenantId: string) => void
  logout: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  token: null,
  tenantId: null,
  isAuthenticated: false,
  login: (user, token, tenantId) => set({ user, token, tenantId, isAuthenticated: true }),
  logout: () => set({ user: null, token: null, tenantId: null, isAuthenticated: false }),
}))
