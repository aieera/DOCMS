import { useAuthStore } from '@/store/authStore'
import * as authApi from '@/api/auth'
import { useNavigate } from '@tanstack/react-router'

// Wave 5 pattern 5: useLogin was deleted in Turn 5 — it was orphaned
// (login.tsx drives all auth flows directly through
// finalizeLoginResult, see H-1) AND carried a `user.tenant_id ?? ''`
// fallback that would have re-introduced the empty-tenant-ID trap
// the moment any new code started consuming it. The handoff doc
// captures the deletion as a follow-up note in case a future
// caller appears; new login flows must route through
// lib/finalizeLogin.ts, not through a re-introduction of this hook.

export function useLogout() {
  const authLogout = useAuthStore((s) => s.logout)
  const navigate = useNavigate()
  return () => { authApi.logout().catch(() => {}); authLogout(); navigate({ to: '/login' }) }
}

export function useIsAuthenticated() {
  return useAuthStore((s) => s.isAuthenticated)
}

export function useCurrentUser() {
  return useAuthStore((s) => s.user)
}
