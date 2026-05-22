import { useAuthStore } from '@/store/authStore'
import * as authApi from '@/api/auth'
import { useNavigate } from '@tanstack/react-router'
import { useAppMutation } from './useAppMutation'

// NOTE: useLogin in this file appears to be orphaned — login.tsx
// drives the password / passkey / MFA flows directly through
// finalizeLoginResult (see H-1). No current consumers; left here
// for now to preserve the public surface and migrated to
// useAppMutation for consistency. It also still has H-1's
// `?? ''` hole (auth store admitting an empty tenant_id) — if any
// future code starts consuming this hook, route it through
// finalizeLoginResult instead. Logged in the handoff as a Wave-5
// follow-up.
export function useLogin() {
  const authLogin = useAuthStore((s) => s.login)
  const navigate = useNavigate()
  return useAppMutation({
    mutationFn: ({ email, password, tenantSlug }: { email: string; password: string; tenantSlug: string }) =>
      authApi.login(email, password, tenantSlug),
    onSuccess: (data) => {
      authLogin(data.user, data.user.tenant_id ?? '')
      navigate({ to: '/' })
    },
    defaultErrorMessage: 'Invalid credentials',
  })
}

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
