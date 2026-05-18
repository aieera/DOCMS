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
      // deciding to redirect. If a hydration is already in flight (kicked
      // off by the axios interceptor's ensureHydrated() during a parallel
      // query), await that one instead of double-firing /auth/me.
      try {
        const pending = state.hydrationPromise
        if (pending) {
          await pending
          user = useAuthStore.getState().user
          if (!user) throw new Error('hydration produced no user')
        } else {
          user = await getCurrentUser()
          // Empty tenant_id would leave X-Auth-Tenant-ID off every
          // subsequent request and trip server-side identity checks
          // (compliance_handler.callers and the same pattern in tasks,
          // ocr_quality, signature, etc.). Treat it as auth failure.
          if (!user.tenant_id) throw new Error('auth/me returned no tenant_id')
          state.login(user, user.tenant_id)
        }
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
