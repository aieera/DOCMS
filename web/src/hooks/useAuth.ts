import { useMutation } from '@tanstack/react-query'
import { useAuthStore } from '@/store/authStore'
import * as authApi from '@/api/auth'
import { useNavigate } from '@tanstack/react-router'
import { toast } from 'sonner'

export function useLogin() {
  const authLogin = useAuthStore((s) => s.login)
  const navigate = useNavigate()
  return useMutation({
    mutationFn: ({ email, password, tenantSlug }: { email: string; password: string; tenantSlug: string }) =>
      authApi.login(email, password, tenantSlug),
    onSuccess: (data) => {
      authLogin(data.user, data.user.tenant_id ?? '')
      navigate({ to: '/' })
    },
    onError: () => toast.error('Invalid credentials'),
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
