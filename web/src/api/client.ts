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

api.interceptors.response.use(
  (r) => r,
  (error) => {
    const status = error.response?.status
    if (status === 401) {
      useAuthStore.getState().logout()
      window.location.href = '/login'
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
