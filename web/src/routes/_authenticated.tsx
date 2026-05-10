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

export const Route = createFileRoute('/_authenticated')({
  beforeLoad: async () => {
    const state = useAuthStore.getState()
    if (state.isAuthenticated) return
    // Store is empty (page refresh or direct URL). The HttpOnly session
    // cookie may still be valid — rehydrate from /auth/me before
    // deciding to redirect.
    try {
      const user = await getCurrentUser()
      state.login(user, user.tenant_id ?? '')
    } catch {
      throw redirect({ to: '/login' })
    }
  },
  component: AuthenticatedLayout,
})
