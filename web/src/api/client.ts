import axios from 'axios'
import { toast } from 'sonner'
import { useAuthStore } from '@/store/authStore'
import type { User } from '@/types/api'

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

// hasSessionCookie returns true when the browser appears to hold a session,
// so we can tell "logged-out user firing a query" (let it 401 fast) apart
// from "logged-in user mid-reload" (wait for /auth/me to populate the store).
//
// It checks dms_csrf, NOT dms_session. The real session cookie dms_session is
// HttpOnly (pkg auth handler setSessionCookie), so document.cookie NEVER
// exposes it to JS — keying off it made this ALWAYS return false in a real
// browser, which collapsed sessionIsDead() to always-true and made every 401
// force a logout+redirect (the "Settings / open document bounces to /login"
// bug). dms_csrf is the non-HttpOnly companion the auth handler sets and
// clears in lockstep with dms_session, so it's the JS-visible session signal.
function hasSessionCookie(): boolean {
  return document.cookie.split(';').some((c) => c.trim().startsWith('dms_csrf='))
}

// M-1: cooldown window after a failed /auth/me. Without it, a
// persistent 500 on the bootstrap call became a storm — every
// queued AJAX request that hit ensureHydrated() retried /auth/me,
// got another 500, and immediately let the next caller through to
// fire another one. This module-level timestamp gates re-entry so
// at most one /auth/me round-trip happens per HYDRATION_BACKOFF_MS.
// On a successful hydration the timestamp is cleared.
const HYDRATION_BACKOFF_MS = 5000
let lastHydrationFailureAt: number | null = null

// ensureHydrated blocks until the auth store knows who the caller is.
// Called from the request interceptor for any non-/auth/me request.
//
// Why this exists: TanStack Router's beforeLoad gates rendering of
// route children, but it does NOT block axios. Code paths that fire
// queries from outside the routed tree — or that race the router's
// async beforeLoad — used to go out with empty
// X-Auth-Tenant-ID / X-User-ID headers, which several backend
// handlers (callers() in compliance_handler.go and friends) treat as
// 401 and which other handlers may surface as 500 on the downstream
// nil-deref. Single-flight via authStore.hydrationPromise.
//
// Failure paths:
//   - No session cookie → return immediately (anonymous/public
//     request). Original request goes out without identity headers
//     and the store stays unauthenticated so nothing leaks.
//   - Inside the back-off window after a recent failure → skip the
//     /auth/me retry. The original request still goes out; if the
//     backend is healthy enough for it, it will succeed (and any
//     identity-required handler returns 401, which the response
//     interceptor logs out on).
//   - /auth/me itself fails → swallow the error, set the back-off
//     timestamp, leave the store unauthenticated. We do NOT call
//     login() with a partial payload, so anonymous-vs-authenticated
//     branching downstream is always honest about state.
async function ensureHydrated(): Promise<void> {
  const state = useAuthStore.getState()
  if (state.isAuthenticated && state.tenantId) return
  if (!hasSessionCookie()) {
    // L-2: anonymous browser. Auth state is settled (definitively
    // not-logged-in), so components gating on isHydrating can render
    // their unauthenticated UI without flashing a placeholder.
    state.markHydrated()
    return
  }
  if (state.hydrationPromise) {
    await state.hydrationPromise
    return
  }
  if (
    lastHydrationFailureAt !== null &&
    Date.now() - lastHydrationFailureAt < HYDRATION_BACKOFF_MS
  ) {
    // Recent failure — don't pile another /auth/me on top of a
    // backend that's already returning 5xx. The next ensureHydrated
    // call after the window will try again.
    return
  }
  const p = (async () => {
    try {
      // Bare axios call — using `api` here would recurse into this
      // interceptor. We still need withCredentials so the cookie ships.
      const { data } = await axios.get<User>('/api/v1/auth/me', { withCredentials: true })
      if (data.tenant_id) {
        useAuthStore.getState().login(data, data.tenant_id)
        lastHydrationFailureAt = null
      } else {
        // Server returned a user record with no tenant_id — same
        // shape that the post-login finalizeLogin guard refuses (H-1).
        // Treat as a failure rather than silently sticking a partial
        // user in the store; back-off so we don't loop on it.
        lastHydrationFailureAt = Date.now()
      }
    } catch {
      // Bootstrap failed (401, 5xx, network). Set the back-off
      // timestamp so the next queued request waits out the window
      // instead of immediately re-attempting. The original request
      // still proceeds; if it needs auth it'll get 401 → the response
      // interceptor logs the user out.
      lastHydrationFailureAt = Date.now()
    } finally {
      // Settle the store regardless of success/failure — L-2: even on
      // a failed hydration the state is "definitively not logged in",
      // not "still checking". login() above already flips isHydrating
      // when it fires; this catches every non-login path (no
      // tenant_id, network failure, 5xx) so the flag never stays
      // stuck at true.
      const s = useAuthStore.getState()
      s.markHydrated()
      s.setHydration(null)
    }
  })()
  useAuthStore.getState().setHydration(p)
  await p
}

// Exposed for tests so they can reset the module-scoped back-off
// timestamp between cases. Not part of the runtime surface.
export function __resetHydrationBackoffForTests(): void {
  lastHydrationFailureAt = null
}

api.interceptors.request.use(async (config) => {
  // Skip self-bootstrap for /auth/me itself and for unauthenticated
  // endpoints (login, register, accept-invite) — none of them require
  // identity headers, and /auth/me IS the hydration.
  const url = config.url ?? ''
  const isAuthEndpoint = url.startsWith('/auth/')
  if (!isAuthEndpoint) {
    await ensureHydrated()
  }
  const { tenantId, user } = useAuthStore.getState()
  // §3.1 / B2.3 sweep — backend handlers now read X-Auth-Tenant-ID
  // (the gateway-injected trusted header). In host dev mode the
  // Vite proxy injects X-Gateway-Signature; with that header
  // present the backend treats X-Auth-* as trusted.
  if (tenantId) {
    config.headers['X-Auth-Tenant-ID'] = tenantId
    // M-7 (Wave 3 audit, security): DO NOT REMOVE THIS DUAL-WRITE
    // YET. The §3.1 / B2.3 migration to X-Auth-Tenant-ID is
    // incomplete — confirmed readers of the LEGACY X-Tenant-ID
    // header (as of 2026-05-22) without an X-Auth-Tenant-ID
    // fallback:
    //
    //   1. pkg/middleware/tenant.go:83 — canonical TenantHTTP
    //      middleware. Used by most Go services; only the ones
    //      that also chain SessionAuth before TenantHTTP get a
    //      cookie-derived fallback. Audit on a per-service basis
    //      before declaring safe.
    //   2. services/intelligence/app/api/routes.py — 21 FastAPI
    //      routes declare `Header(None, alias="X-Tenant-ID")` with
    //      no fallback. Removing the header would 400-bomb the
    //      entire AI surface: /ask, /qa(+/sync,/history), /rag/query
    //      (+/feedback), /summarize, /translate, /translations/*,
    //      /language/*, /redact/{detect,apply}, /anomaly/run,
    //      /llm/completions, /workspaces/{id}/ai-settings GET+PUT.
    //   3. services/document/internal/handler/storage_proxy.go:316
    //      — direct read via middleware.TenantHeader on the upload-
    //      initiate gRPC-metadata-propagation path.
    //   4. pkg/gateway/ratelimit.go:49 — token-bucket keying. Silent
    //      degradation (all anonymous traffic in one bucket) rather
    //      than outage if removed.
    //   5. pkg/gateway/cors.go:33 — CORS allowlist check.
    //   6. pkg/metrics/metrics.go:130 — Prometheus tenant label.
    //
    // The header is currently mitigated by RequireGatewaySignature
    // (only Kong-or-Vite-proxy-signed traffic reaches backends), so
    // it's "trusted via the signed gateway boundary" rather than
    // "trusted unconditionally". Still architectural debt, but not
    // a live unmitigated risk.
    //
    // Safe removal sequence (separate backend track, not in FE):
    //   a. intelligence: extract one FastAPI dependency that reads
    //      X-Auth-Tenant-ID first, then X-Tenant-ID, then 400. Swap
    //      all 21 routes to use it.
    //   b. pkg/middleware/tenant.go: TenantHTTP reads
    //      X-Auth-Tenant-ID first, falls back to X-Tenant-ID, then
    //      auth-context. One change covers every Go consumer of
    //      the const.
    //   c. services/document/storage_proxy.go: same dual-read.
    //   d. pkg/gateway/{cors,ratelimit}.go + pkg/metrics/metrics.go:
    //      same dual-read.
    //   e. Deploy. Verify every prod replica is on the new code.
    //   f. ONE release later, remove this line. Bump the comment.
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

// ---- status-string humanization (BUG-31) ---------------------------
//
// Backend status codes leak to users verbatim. pkg/errors ToGRPCError
// sends KindValidation as `status.Error(codes.InvalidArgument,
// e.Error())`, and Error() formats as "<CODE>: <message>" — so the
// grpc-gateway body is {"code":3,"message":"INVALID_ARGUMENT: required"}
// and the toast read literally "INVALID_ARGUMENT: required". The direct
// HTTP handlers emit the same vocabulary through pkg/errors HTTPError
// ({type, message}).
//
// This is deliberately a SMALL, GENERAL map keyed on the shared status
// vocabulary — one entry per pkg/errors Code plus the canonical gRPC
// code names — NOT a per-endpoint dictionary. Anything we don't
// recognize is passed through untouched rather than mangled.
const STATUS_SENTENCES: Record<string, string> = {
  // pkg/errors codes
  INVALID_ARGUMENT: 'That request was rejected as invalid.',
  NOT_FOUND: "We couldn't find that — it may have been moved or deleted.",
  ALREADY_EXISTS: 'That already exists.',
  FORBIDDEN: "You don't have permission to do that.",
  UNAUTHORIZED: 'Your session has expired — sign in again.',
  CONFLICT: 'Someone else changed this first — reload and try again.',
  LEGAL_HOLD: 'This document is under legal hold and cannot be changed.',
  RECORD_DECLARED: 'This document is a declared record — it stays immutable until disposition.',
  WORM_LOCKED: 'This document is locked until its retention date.',
  REGION_VIOLATION: 'That would move data outside its pinned region.',
  RATE_LIMITED: 'Too many requests — try again in a moment.',
  INTERNAL: 'Something went wrong on the server.',
  // canonical gRPC code names (grpc-gateway / non-domain errors)
  PERMISSION_DENIED: "You don't have permission to do that.",
  UNAUTHENTICATED: 'Your session has expired — sign in again.',
  FAILED_PRECONDITION: "That isn't allowed in this item's current state.",
  ABORTED: 'Someone else changed this first — reload and try again.',
  RESOURCE_EXHAUSTED: 'Too many requests — try again in a moment.',
  DEADLINE_EXCEEDED: 'The server took too long to respond — try again.',
  UNAVAILABLE: 'That service is temporarily unavailable — try again.',
  UNIMPLEMENTED: "That isn't supported yet.",
  OUT_OF_RANGE: 'That value is out of range.',
  CANCELLED: 'The request was cancelled.',
  UNKNOWN: 'Something went wrong on the server.',
  DATA_LOSS: 'Something went wrong on the server.',
}

// The validation messages pkg/errors emits are terse field-level
// fragments ("required", "not a uuid"). Appending them to the sentence
// above reads worse than replacing it, so the handful that are truly
// generic (not endpoint-specific) get their own sentence.
const TERSE_DETAIL_SENTENCES: Record<string, string> = {
  required: 'A required value was missing.',
  'not a uuid': "That identifier isn't valid.",
  'invalid uuid': "That identifier isn't valid.",
  'malformed cursor': 'That page link is no longer valid — reload the list.',
}

const STATUS_PREFIX_RE = /^([A-Z][A-Z0-9_]{2,}):\s*(.*)$/s

/** Rewrite a "<STATUS_CODE>: <detail>" backend string as a human
 *  sentence. Strings that don't carry a status prefix — or carry one we
 *  don't know — come back unchanged. */
export function humanizeStatusMessage(raw: string): string {
  const m = STATUS_PREFIX_RE.exec(raw.trim())
  if (!m) return raw
  const [, code, rest] = m
  const sentence = STATUS_SENTENCES[code]
  if (!sentence) return raw
  const detail = rest.trim()
  const terse = TERSE_DETAIL_SENTENCES[detail.toLowerCase()]
  if (terse) return terse
  // A detail that is itself a sentence carries more information than
  // the generic lead-in, so prefer it and keep the lead-in as context.
  if (!detail || detail.toLowerCase() === code.toLowerCase()) return sentence
  return `${sentence} (${detail})`
}

// readErrorMessage extracts a human-readable message from an axios
// error response. Tries the common shapes our backend services emit
// — { error }, { message }, { detail }, FieldError envelopes — then
// falls back to the raw status text. Every extracted string is run
// through humanizeStatusMessage so a raw gRPC/domain status code never
// reaches a toast. Surfaced in toasts so the user (or QA) sees what the
// server actually rejected, not just "400". Exported so route/component
// error handlers can stop reaching into `(e as any).response.data.error`
// chains (Wave 5 pattern 4).
export function readErrorMessage(err: unknown): string | null {
  const raw = rawErrorMessage(err)
  return raw === null ? null : humanizeStatusMessage(raw)
}

function rawErrorMessage(err: unknown): string | null {
  if (!err || typeof err !== 'object') return null
  const data = (err as { response?: { data?: unknown } }).response?.data
  if (typeof data === 'string') return data
  if (data && typeof data === 'object') {
    const d = data as Record<string, unknown>
    if (typeof d.error === 'string') return d.error
    // License middleware envelope (pkg/middleware/license.go):
    // { error: { code, message } } — nested object, not a string.
    if (d.error && typeof d.error === 'object') {
      const ne = d.error as { code?: string; message?: string }
      if (typeof ne.message === 'string') return ne.message
    }
    // pkg/errors HTTPError envelope: {type: "<CODE>", message: "<detail>"}.
    // Re-joining them lets humanizeStatusMessage see the code it needs —
    // reading `message` alone surfaced bare fragments like "required".
    if (typeof d.type === 'string' && typeof d.message === 'string') {
      return `${d.type}: ${d.message}`
    }
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

// toastError — an error toast that collapses duplicates instead of
// stacking them. sonner keys toasts by id, so reusing an id derived
// from the message replaces the live toast rather than adding an
// identical second one. Without this, a page whose queries all fail
// with the same backend error (e.g. every request against a
// non-existent workspace id) buried the viewport under a column of
// identical toasts.
export function toastError(message: string, options?: Parameters<typeof toast.error>[1]): void {
  toast.error(message, { id: `err:${message}`, ...options })
}

// sessionIsDead answers the question a bare 401 can't: did OUR SeDoc
// session actually expire, or did a downstream/integration endpoint
// (e.g. the ERP Files-BFF proxy under /integrations/erp/*) return 401
// for its own reasons while our session is perfectly valid? The
// authoritative probe is a bare /auth/me — if it still returns a user
// with a tenant, the session lives and the 401 was endpoint-specific.
// Single-flighted so a burst of parallel 401s (a page's queries racing
// on a hard reload) fires at most one probe.
let sessionProbe: Promise<boolean> | null = null
async function sessionIsDead(): Promise<boolean> {
  if (!hasSessionCookie()) return true
  if (!sessionProbe) {
    sessionProbe = (async () => {
      try {
        // Bare axios (not `api`) so this probe never recurses back
        // through the interceptor below.
        const { data } = await axios.get<User>('/api/v1/auth/me', { withCredentials: true })
        return !data?.tenant_id
      } catch (probeErr) {
        // Only an explicit 401 from /auth/me proves the session is gone.
        // A network error, timeout, 5xx, or gateway 502 means the probe
        // never got an authoritative answer — treating those as "dead"
        // destroyed a valid session on every transient backend blip
        // (the intermittent "auto-logged-out mid-task, retry works" bug).
        return axios.isAxiosError(probeErr) && probeErr.response?.status === 401
      }
    })().finally(() => {
      sessionProbe = null
    })
  }
  return sessionProbe
}

api.interceptors.response.use(
  (r) => r,
  async (error) => {
    const status = error.response?.status
    const detail = readErrorMessage(error)
    if (status === 401) {
      // A 401 is NOT proof the session died. Force-logging-out on every
      // 401 meant a single feature endpoint — notably the ERP
      // integration proxy, which passes the ERP BFF's 401 straight
      // through — could silently destroy a valid session and bounce the
      // user to /login with no warning, unrecoverably (BUG: "Open" on
      // the ERP card logs you out). Only tear the session down when a
      // bare /auth/me CONFIRMS it's actually gone. /auth/* calls are the
      // real identity signal (and getCurrentUser() rides `api`), so a
      // 401 there IS a genuine expiry — skip the probe to avoid looping.
      const url = error.config?.url ?? ''
      const isAuthProbe = url.startsWith('/auth/')
      if (!isAuthProbe && !(await sessionIsDead())) {
        toastError(detail || 'Not authorized for that request.')
        return Promise.reject(error)
      }
      useAuthStore.getState().logout()
      window.location.href = '/login'
    } else if (status === 403) {
      // Speculative/background reads (dashboard probes that are
      // EXPECTED to 403 for non-admin roles) opt out via
      // `suppressErrorToast` — a red "Access denied" on page load for
      // a widget the user never asked for reads as breakage.
      if (!(error.config as { suppressErrorToast?: boolean } | undefined)?.suppressErrorToast) {
        toastError(detail ? `Access denied — ${detail}` : 'Access denied')
      }
    } else if (status === 402) {
      // ADR 0095 RequireLicenseFeature — feature not in the license.
      toastError(detail ?? 'This feature is not included in your license.')
    } else if (status === 423) {
      // ADR 0095 LicenseWriteGate — grace/expired license locks writes.
      toastError(detail ?? 'License expired — writes are locked. Renew to restore write access.')
    } else if (status === 429) {
      toastError('Rate limited — try again in a moment')
    } else if (status && status >= 500) {
      toastError(detail ? `Server error: ${detail}` : 'Server error — please retry')
    } else if (status === 400 && detail) {
      // Validation errors weren't surfaced before — toast the first
      // field error / message so the user sees what to fix instead
      // of a silent failure.
      toastError(detail)
    }
    return Promise.reject(error)
  },
)
