import axios from 'axios'
import { toast } from 'sonner'
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

// readErrorMessage extracts a human-readable message from an axios
// error response. Tries the common shapes our backend services emit
// — { error }, { message }, { detail }, FieldError envelopes — then
// falls back to the raw status text. Surfaced in toasts so the user
// (or QA) sees what the server actually rejected, not just "400".
function readErrorMessage(err: unknown): string | null {
  if (!err || typeof err !== 'object') return null
  const data = (err as { response?: { data?: unknown } }).response?.data
  if (typeof data === 'string') return data
  if (data && typeof data === 'object') {
    const d = data as Record<string, unknown>
    if (typeof d.error === 'string') return d.error
    if (typeof d.message === 'string') return d.message
    if (typeof d.detail === 'string') return d.detail
    if (Array.isArray(d.field_errors) && d.field_errors.length > 0) {
      const fe = d.field_errors[0] as { field?: string; message?: string }
      if (fe.field && fe.message) return `${fe.field}: ${fe.message}`
    }
    if (d.code && typeof d.code === 'string' && d.message) {
      return `${d.code}: ${d.message}`
    }
  }
  return null
}

api.interceptors.response.use(
  (r) => r,
  (error) => {
    const status = error.response?.status
    const detail = readErrorMessage(error)
    if (status === 401) {
      useAuthStore.getState().logout()
      window.location.href = '/login'
    } else if (status === 403) {
      toast.error(detail ? `Access denied — ${detail}` : 'Access denied')
    } else if (status === 429) {
      toast.error('Rate limited — try again in a moment')
    } else if (status && status >= 500) {
      toast.error(detail ? `Server error: ${detail}` : 'Server error — please retry')
    } else if (status === 400 && detail) {
      // Validation errors weren't surfaced before — toast the first
      // field error / message so the user sees what to fix instead
      // of a silent failure.
      toast.error(detail)
    }
    return Promise.reject(error)
  },
)
