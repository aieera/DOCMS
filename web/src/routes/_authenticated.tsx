import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'
import { AppLayout } from '@/components/layout/app-layout'
import { useAuthStore } from '@/store/authStore'
import { getCurrentUser } from '@/api/auth'

function AuthenticatedLayout() {
  return (
    <AppLayout>
      <Outlet />
    </AppLayout>
  )
}

// Roles allowed past the /admin gate. Mirrors the backend's
// RequireRole("admin", "owner") on every /api/v1/admin/* mount —
// kept in sync deliberately so a Member can't see an empty UI shell
// (BUG-B) while the API quietly 403s every request behind it.
const ADMIN_ROLES = new Set(['admin', 'owner'])

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: async ({ location }) => {
    const state = useAuthStore.getState()
    let user = state.user
    if (!state.isAuthenticated) {
      // Store is empty (page refresh or direct URL). The HttpOnly session
      // cookie may still be valid — rehydrate from /auth/me before
      // deciding to redirect.
      try {
        user = await getCurrentUser()
        state.login(user, user.tenant_id ?? '')
      } catch {
        throw redirect({ to: '/login' })
      }
    }
    if (location.pathname.startsWith('/admin')) {
      const role = user?.role ?? ''
      const isPlatformAdmin = user?.is_platform_admin === true
      if (!ADMIN_ROLES.has(role) && !isPlatformAdmin) {
        throw redirect({ to: '/' })
      }
    }
  },
  component: AuthenticatedLayout,
})
