import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import { api } from '@/api/client'
import { runPersistedQuery } from '@/lib/graphql'
import { streamQA } from '@/api/doc-qa'
import { useAuthStore } from '@/store/authStore'

// DEC-3 (client half): identity comes from the HttpOnly dms_session
// cookie. Servers ignore X-Tenant-ID / X-Auth-Tenant-ID / X-User-*
// unless an internal service key is present, and the dev proxy strips
// them — so the browser must never send them. Sending them only invited
// a forged-identity path through any proxy that forwarded them.
const IDENTITY_HEADERS = [
  'x-tenant-id',
  'x-auth-tenant-id',
  'x-auth-user-id',
  'x-user-id',
  'x-user-role',
  'x-user-email',
  'x-group-ids',
]

const AUTHED = {
  user: { id: 'u-1', email: 'me@example.com', role: 'owner', tenant_id: 't-1' },
  tenantId: 't-1',
  isAuthenticated: true,
  isHydrating: false,
  hydrationPromise: null,
}

function headerNames(h: HeadersInit | undefined): string[] {
  return [...new Headers(h ?? {}).keys()].map((k) => k.toLowerCase())
}

describe('DEC-3 — web client sends no identity headers', () => {
  beforeEach(() => {
    document.cookie = 'dms_csrf=test-csrf'
    useAuthStore.setState({ ...AUTHED } as never)
  })
  afterEach(() => {
    document.cookie = 'dms_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT'
    vi.restoreAllMocks()
  })

  it('axios api requests carry no identity headers (GET and mutating)', async () => {
    const seen: string[][] = []
    server.use(
      http.get('*/api/v1/documents', ({ request }) => {
        seen.push([...request.headers.keys()])
        return HttpResponse.json({ documents: [] })
      }),
      http.post('*/api/v1/documents', ({ request }) => {
        seen.push([...request.headers.keys()])
        return HttpResponse.json({ id: 'd-1' })
      }),
    )
    await api.get('/documents')
    await api.post('/documents', { title: 'x' })
    expect(seen).toHaveLength(2)
    for (const keys of seen) {
      for (const h of IDENTITY_HEADERS) expect(keys).not.toContain(h)
    }
    // CSRF echo still present on the mutating call.
    expect(seen[1]).toContain('x-csrf-token')
  })

  it('GraphQL persisted queries carry no identity headers', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response(JSON.stringify({ data: {} }), { status: 200 }))
    await runPersistedQuery({ name: 'Op', hash: 'abc' } as never, {})
    const init = fetchSpy.mock.calls[0][1] as RequestInit
    expect(init.credentials).toBe('include')
    const names = headerNames(init.headers)
    for (const h of IDENTITY_HEADERS) expect(names).not.toContain(h)
  })

  it('Doc Q&A SSE stream carries no identity headers', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response(new ReadableStream({ start: (c) => c.close() }), { status: 200 }))
    await streamQA({ documentId: 'd-1', question: 'q', onEvent: () => {} })
    const init = fetchSpy.mock.calls[0][1] as RequestInit
    expect(init.credentials).toBe('include')
    const names = headerNames(init.headers)
    for (const h of IDENTITY_HEADERS) expect(names).not.toContain(h)
  })
})
