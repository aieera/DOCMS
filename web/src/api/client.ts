import axios from 'axios'
import toast from 'react-hot-toast'
import { useAuthStore } from '@/store/authStore'

// withCredentials: true lets the browser include the HttpOnly session
// cookie on every request. The cookie is set by the auth service on login
// and is the only authentication mechanism — no Authorization header is
// set by the frontend.
export const api = axios.create({ baseURL: '/api/v1', withCredentials: true })

// Read the (non-HttpOnly) dms_csrf cookie. Paired with the backend's
// CSRF double-submit middleware — see pkg/middleware/csrf.go.
function readCookie(name: string): string {
  const prefix = name + '='
  for (const part of document.cookie.split(';')) {
    const trimmed = part.trim()
    if (trimmed.startsWith(prefix)) return decodeURIComponent(trimmed.slice(prefix.length))
  }
  return ''
}

const SAFE_METHODS = new Set(['get', 'head', 'options'])

api.interceptors.request.use((config) => {
  const { tenantId, user } = useAuthStore.getState()
  // §3.1 / B2.3 sweep — backend handlers now read X-Auth-Tenant-ID
  // (the gateway-injected trusted header). In host dev mode the
  // Vite proxy injects X-Gateway-Signature; with that header
  // present the backend treats X-Auth-* as trusted.
  if (tenantId) {
    config.headers['X-Auth-Tenant-ID'] = tenantId
    // Retain the legacy header for a release so any handler that
    // hasn't finished the sweep still receives it. Remove after
    // confirming nothing reads it.
    config.headers['X-Tenant-ID'] = tenantId
  }
  // Wave 11.2: role header for OPA-gated endpoints (e.g. legal holds
  // require compliance_officer / admin / owner). Downstream services
  // that don't care about role simply ignore the header.
  if (user?.id) config.headers['X-User-ID'] = user.id
  if (user?.role) config.headers['X-User-Role'] = user.role

  // CSRF double-submit: echo the dms_csrf cookie as X-CSRF-Token on
  // every mutating request. Safe methods are skipped; the backend
  // exempts them regardless.
  const method = (config.method || 'get').toLowerCase()
  if (!SAFE_METHODS.has(method)) {
    const csrf = readCookie('dms_csrf')
    if (csrf) config.headers['X-CSRF-Token'] = csrf
  }
  return config
})

// The only endpoint that authoritatively reports session-invalid is
// `/auth/me` — a 401 there means the session cookie is gone or
// expired. 401s from any OTHER endpoint can mean many things
// (missing tenant header, service-specific auth failure, transient
// upstream) and should NOT blanket-logout the user — they'd lose
// their session on a single flaky request.
//
// Prior behaviour: any 401 anywhere → logout + redirect. That made
// "click dashboard card → navigate to /workspaces → GET /workspaces
// 401s for a root cause → user kicked to /login" a frequent dev
// surprise. Fixed 2026-04-20.
const SESSION_VALIDATION_PATHS = ['/auth/me']

api.interceptors.response.use(
  (r) => r,
  (error) => {
    const status = error.response?.status
    const url = error.config?.url || ''
    if (status === 401) {
      if (SESSION_VALIDATION_PATHS.some((p) => url.includes(p))) {
        // Actual session expiry or invalidation.
        useAuthStore.getState().logout()
        window.location.href = '/login'
      } else {
        // Per-request auth failure — session may still be valid.
        // Surface the error and let the caller (or the user) decide.
        toast.error('Request unauthorised — if this persists, sign in again')
      }
    } else if (status === 428) {
      // Wave 15.2 step-up challenge. The geofence middleware sets
      // `WWW-Authenticate: Step-Up` when the policy requires re-MFA
      // before the request can proceed. Surface a toast and point
      // the user at the challenge path; full MFA challenge UX is a
      // Wave 15.2 follow-up.
      toast.error('Additional verification required for this location. Re-authenticate and try again.')
    } else if (status === 451) {
      // Wave 15.2 geofence deny. 451 Unavailable For Legal Reasons
      // is the canonical code for tenant / country / CIDR blocks.
      const reason = error.response?.headers?.['x-geofence-reason'] || ''
      toast.error(`Request blocked by geofence policy${reason ? ` (${reason})` : ''}`)
    } else if (status === 403) {
      toast.error('Access denied')
    } else if (status === 429) {
      toast.error('Rate limited — try again in a moment')
    } else if (status && status >= 500) {
      toast.error('Server error — please retry')
    }
    return Promise.reject(error)
  },
)
