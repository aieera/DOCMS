import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { api, __resetHydrationBackoffForTests } from '@/api/client'
import { useAuthStore } from '@/store/authStore'

// M-1 hydration back-off invariant (client.ts ensureHydrated).
//
// The request interceptor calls ensureHydrated() before every non-/auth/
// request. When the caller is logged-out-but-has-a-session-cookie (mid
// page reload), ensureHydrated fires a single /auth/me to populate the
// store. If that call FAILS, a module-level timestamp gates re-entry so
// at most one /auth/me round-trip happens per HYDRATION_BACKOFF_MS —
// without it a persistent 5xx turned every queued request into another
// /auth/me retry (the storm M-1 fixed).
//
// HYDRATION_BACKOFF_MS is 5000 (client.ts). The test pins the two halves
// of the invariant: (1) at most one /auth/me inside the window, (2) one
// more after it elapses.

// client.ts gates the /auth/me fetch behind hasSessionCookie(), which
// reads document.cookie for `dms_session=`.
function setSessionCookie() {
  document.cookie = 'dms_session=test-session'
}
function clearSessionCookie() {
  // Expire it. jsdom has no API to delete cookies; an immediate expiry
  // is the standard idiom.
  document.cookie = 'dms_session=; expires=Thu, 01 Jan 1970 00:00:00 GMT'
}

describe('api/client — hydration back-off (M-1)', () => {
  let meCalls = 0

  beforeEach(() => {
    vi.useFakeTimers()
    meCalls = 0
    __resetHydrationBackoffForTests()
    // Reset to a clean logged-out store so ensureHydrated doesn't take
    // the `isAuthenticated && tenantId` early-return.
    useAuthStore.setState({
      user: null,
      tenantId: null,
      isAuthenticated: false,
      isHydrating: true,
      hydrationPromise: null,
    })
    setSessionCookie()
    // /auth/me always fails so the back-off engages; count every hit.
    // A non-/auth/ endpoint (/documents) is the carrier request that
    // drives ensureHydrated through the interceptor.
    server.use(
      http.get('*/api/v1/auth/me', () => {
        meCalls += 1
        return HttpResponse.json({ error: 'boom' }, { status: 500 })
      }),
      http.get('*/api/v1/documents', () =>
        HttpResponse.json({ items: [], total_count: 0, page_token: '' }),
      ),
    )
  })

  afterEach(() => {
    vi.useRealTimers()
    clearSessionCookie()
    __resetHydrationBackoffForTests()
  })

  // Fire a carrier request through the interceptor and let the
  // ensureHydrated promise (and any /auth/me it triggers) settle. We
  // swallow carrier failures — the assertion is on meCalls, not on the
  // carrier response.
  async function triggerHydration() {
    const p = api.get('/documents').catch(() => {})
    // Flush microtasks so the awaited axios.get('/auth/me') inside
    // ensureHydrated resolves under fake timers.
    await vi.runAllTimersAsync()
    await p
  }

  it('fires at most one /auth/me within HYDRATION_BACKOFF_MS', async () => {
    await triggerHydration()
    expect(meCalls).toBe(1)

    // Several more carrier requests, all still inside the 5s window.
    await vi.advanceTimersByTimeAsync(1000)
    await triggerHydration()
    await vi.advanceTimersByTimeAsync(1000)
    await triggerHydration()
    await vi.advanceTimersByTimeAsync(1000)
    await triggerHydration()

    // Still only the original /auth/me — the back-off suppressed the
    // retries while the failure timestamp is fresh.
    expect(meCalls).toBe(1)
  })

  it('fires one more /auth/me after the window elapses', async () => {
    await triggerHydration()
    expect(meCalls).toBe(1)

    // Cross the back-off window. HYDRATION_BACKOFF_MS is 5000, so 5001ms
    // puts us strictly past it.
    await vi.advanceTimersByTimeAsync(5001)
    await triggerHydration()

    expect(meCalls).toBe(2)
  })
})
