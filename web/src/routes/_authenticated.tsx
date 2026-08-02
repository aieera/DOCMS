import { createFileRoute, Outlet, redirect } from '@tanstack/react-router'
import { AppLayout } from '@/components/layout/app-layout'
import { useAuthStore } from '@/store/authStore'
import { getCurrentUser } from '@/api/auth'
import { DuplicateUploadDialog } from '@/components/documents/DuplicateUploadDialog'

function AuthenticatedLayout() {
  return (
    <AppLayout>
      <Outlet />
      {/* Global mount: the upload flow (useUpload) raises a duplicate
          prompt via the upload store from any page, so the dialog lives
          here rather than at each upload call site. */}
      <DuplicateUploadDialog />
    </AppLayout>
  )
}

// Roles allowed past the /admin gate. Mirrors the backend's
// RequireRole("admin", "owner") on every /api/v1/admin/* mount —
// kept in sync deliberately so a Member can't see an empty UI shell
// (BUG-B) while the API quietly 403s every request behind it.
const ADMIN_ROLES = new Set(['admin', 'owner'])

// Pages a compliance_officer may open in addition to the admin/owner
// set — exactly the surfaces whose Go handlers grant that role read
// access (auto_tag, ocr_quality, compliance_pii, anomaly handlers all
// requireRole(..., "compliance_officer")). '/admin' itself is included
// so the hub can render their (filtered) card set; every other
// /admin/* path still bounces them. Writes remain admin/owner-gated
// server-side, so these pages are effectively read-only for the role
// (ocr-config renders an explicit read-only banner).
export const COMPLIANCE_OFFICER_ADMIN_PATHS = [
  '/admin',
  '/admin/ocr',
  '/admin/pii-scanning',
  '/admin/tagging',
  '/admin/intelligence/anomalies',
]

export function adminPathAllowsComplianceOfficer(pathname: string): boolean {
  return COMPLIANCE_OFFICER_ADMIN_PATHS.some((p) =>
    // '/admin' is EXACT-match only (it's the hub); a prefix match there
    // would silently open every /admin/* page to the role.
    p === '/admin' ? pathname === '/admin' : pathname === p || pathname.startsWith(p + '/'),
  )
}

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
      const complianceAllowed =
        role === 'compliance_officer' && adminPathAllowsComplianceOfficer(location.pathname)
      if (!ADMIN_ROLES.has(role) && !isPlatformAdmin && !complianceAllowed) {
        throw redirect({ to: '/' })
      }
    }
  },
  component: AuthenticatedLayout,
})
