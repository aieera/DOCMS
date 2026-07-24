import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { api } from '@/api/client'
import { useAuthStore } from '@/store/authStore'

// Regression guard for the ERP force-logout bug (and the hard-nav auth
// race that shared its cause): a 401 from a FEATURE endpoint must NOT
// tear down a still-valid session. The response interceptor now probes
// /auth/me before logging out — if the session is alive, the 401 is
// surfaced (toast) and the store/URL are left intact.

// Mirror the browser exactly: the real session cookie `dms_session` is
// HttpOnly and therefore INVISIBLE to document.cookie — only its
// non-HttpOnly companion `dms_csrf` (set/cleared in lockstep by the auth
// handler) is readable by JS. A previous version of this test set
// `dms_session` via document.cookie, which jsdom allows but a real browser
// never would, so the session-presence check passed in tests while it
// always returned false in production (every 401 → forced logout).
function setSessionCookie() {
  document.cookie = 'dms_csrf=test-csrf'
}
function clearSessionCookie() {
  document.cookie = 'dms_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT'
}

// window.location.href assignment throws "not implemented" in jsdom;
// replace it with a plain object we can assert on. It MUST keep a valid
// absolute href/origin — the interceptor's /auth/me probe uses a
// relative URL that XHR resolves against location, and an empty base
// makes MSW miss the mock (the session-probe silently 200s).
const ORIGIN = 'http://localhost:3000'
let hrefSpy: { value: string }
function stubLocation() {
  hrefSpy = { value: `${ORIGIN}/` }
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      get href() {
        return hrefSpy.value
      },
      set href(v: string) {
        hrefSpy.value = v
      },
      origin: ORIGIN,
      protocol: 'http:',
      host: 'localhost:3000',
      hostname: 'localhost',
      port: '3000',
      pathname: '/',
      search: '',
      hash: '',
      assign: (v: string) => {
        hrefSpy.value = v
      },
      replace: (v: string) => {
        hrefSpy.value = v
      },
      toString() {
        return hrefSpy.value
      },
    },
  })
}

const AUTHED = {
  user: { id: 'u-1', email: 'me@example.com', role: 'owner', tenant_id: 't-1' },
  tenantId: 't-1',
  isAuthenticated: true,
  isHydrating: false,
  hydrationPromise: null,
}

describe('api/client — 401 does not destroy a valid session (ERP bug)', () => {
  beforeEach(() => {
    setSessionCookie()
    stubLocation()
    useAuthStore.setState({ ...AUTHED } as never)
  })
  afterEach(() => {
    clearSessionCookie()
    vi.restoreAllMocks()
  })

  it('keeps the session when /auth/me still validates on a feature 401', async () => {
    server.use(
      // The ERP proxy passes the BFF's 401 straight through.
      http.get('*/api/v1/integrations/erp/customers', () =>
        HttpResponse.json({ error: 'erp bff rejected' }, { status: 401 }),
      ),
      // But our SeDoc session is perfectly valid.
      http.get('*/api/v1/auth/me', () =>
        HttpResponse.json({ id: 'u-1', email: 'me@example.com', role: 'owner', tenant_id: 't-1' }),
      ),
    )

    await expect(api.get('/integrations/erp/customers')).rejects.toBeDefined()

    // Session intact, NO hard redirect to /login.
    expect(useAuthStore.getState().isAuthenticated).toBe(true)
    expect(hrefSpy.value).not.toBe('/login')
  })

  it('logs out and redirects when the session is genuinely gone', async () => {
    server.use(
      http.get('*/api/v1/documents', () =>
        HttpResponse.json({ error: 'nope' }, { status: 401 }),
      ),
      // /auth/me also 401s — the session really expired.
      http.get('*/api/v1/auth/me', () =>
        HttpResponse.json({ error: 'expired' }, { status: 401 }),
      ),
    )

    await expect(api.get('/documents')).rejects.toBeDefined()

    expect(useAuthStore.getState().isAuthenticated).toBe(false)
    expect(hrefSpy.value).toBe('/login')
  })
})
