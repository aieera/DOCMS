import { http, HttpResponse } from 'msw'
import type { User } from '@/types/api'

// Base URL matches the axios client's baseURL (`/api/v1`). MSW matches
// relative paths in jsdom because axios resolves them against the
// current document's origin.

const API = '*/api/v1'

// Canonical fixtures reused across tests.
export const fixtures = {
  tenantId: 'aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa',
  userId: '7bb83dcf-82a4-4b22-a212-57dc52c78baf',
  user: {
    id: '7bb83dcf-82a4-4b22-a212-57dc52c78baf',
    tenant_id: 'aaaaaaaa-aaaa-7aaa-aaaa-aaaaaaaaaaaa',
    email: 'admin@acme.local',
    display_name: 'Admin User',
    role: 'owner',
    status: 'active',
    mfa_enabled: false,
    created_at: '2026-04-17T00:00:00Z',
  } satisfies User,
}

// Default handlers — happy-path responses for every endpoint the frontend
// calls. Individual tests override specific handlers via server.use() to
// simulate errors, MFA challenges, empty responses, etc.
export const handlers = [
  // ---- Auth -------------------------------------------------------------
  http.post(`${API}/auth/login`, async ({ request }) => {
    const body = (await request.json()) as { email?: string; password?: string }
    if (!body.email || !body.password) {
      return HttpResponse.json({ error: 'missing credentials' }, { status: 400 })
    }
    return HttpResponse.json({
      user: fixtures.user,
      session_token: 'test-session-token',
      tenant_id: fixtures.tenantId,
    })
  }),
  http.post(`${API}/auth/register`, async () =>
    HttpResponse.json({ user_id: fixtures.userId, email: fixtures.user.email, display_name: 'New User', tenant_id: fixtures.tenantId }, { status: 201 }),
  ),
  http.post(`${API}/auth/logout`, () => HttpResponse.json({}, { status: 204 })),
  http.get(`${API}/auth/me`, () => HttpResponse.json(fixtures.user)),
  http.post(`${API}/auth/mfa/verify`, () =>
    HttpResponse.json({ user: fixtures.user, session_token: 'test-session-token', tenant_id: fixtures.tenantId }),
  ),

  // ---- Admin ------------------------------------------------------------
  http.get(`${API}/admin/users`, () =>
    HttpResponse.json({ users: [fixtures.user], next_cursor: '' }),
  ),
  http.post(`${API}/admin/users/invite`, () =>
    HttpResponse.json(
      { user: { ...fixtures.user, email: 'invitee@acme.local' }, invite_token: 'tok-123' },
      { status: 201 },
    ),
  ),
  http.post(`${API}/admin/users/:id/suspend`, () => HttpResponse.json({}, { status: 204 })),
  http.post(`${API}/admin/users/:id/reset-mfa`, () => HttpResponse.json({}, { status: 204 })),
  http.get(`${API}/admin/settings`, () =>
    HttpResponse.json({
      ai_enabled: true,
      advanced_workflow: false,
      sso_enabled: false,
      e_signatures: false,
      custom_branding: false,
      api_access: true,
      data_rooms: false,
    }),
  ),
  http.put(`${API}/admin/settings`, async ({ request }) => HttpResponse.json(await request.json())),

  // ---- Documents --------------------------------------------------------
  http.get(`${API}/documents`, () =>
    HttpResponse.json({ items: [], total_count: 0, page_token: '' }),
  ),
  http.get(`${API}/documents/:id`, ({ params }) =>
    HttpResponse.json({
      id: params.id,
      title: 'Test Document',
      tenant_id: fixtures.tenantId,
      created_at: '2026-04-17T00:00:00Z',
    }),
  ),
  http.patch(`${API}/documents/:id`, async ({ params, request }) =>
    HttpResponse.json({ id: params.id, ...(await request.json() as object) }),
  ),
  http.delete(`${API}/documents/:id`, () => HttpResponse.json({}, { status: 204 })),
  http.get(`${API}/documents/:id/versions`, () => HttpResponse.json([])),

  // ---- Workspaces + folders --------------------------------------------
  http.get(`${API}/workspaces`, () =>
    HttpResponse.json([{ id: 'ws-1', name: 'Default Workspace', tenant_id: fixtures.tenantId }]),
  ),
  http.post(`${API}/workspaces`, async ({ request }) => {
    const body = (await request.json()) as Record<string, unknown>
    return HttpResponse.json({ id: 'ws-new', ...body }, { status: 201 })
  }),
  http.get(`${API}/workspaces/:id/folders`, () => HttpResponse.json([])),
  http.post(`${API}/workspaces/:id/folders`, async ({ request }) => {
    const body = (await request.json()) as Record<string, unknown>
    return HttpResponse.json({ id: 'folder-new', ...body }, { status: 201 })
  }),

  // ---- Search -----------------------------------------------------------
  http.post(`${API}/search`, () =>
    HttpResponse.json({
      hits: [],
      total_count: 0,
      took_ms: 12,
      facets: {},
    }),
  ),
  http.get(`${API}/search/autocomplete`, ({ request }) => {
    const url = new URL(request.url)
    const q = url.searchParams.get('q') ?? ''
    return HttpResponse.json({
      suggestions: q.length > 0 ? [{ text: `${q} contract`, source: 'title' }] : [],
    })
  }),

  // ---- Permissions ------------------------------------------------------
  http.get(`${API}/permissions/:type/:id`, () => HttpResponse.json([])),
  http.post(`${API}/permissions/check`, () => HttpResponse.json({ allowed: true, reason: '' })),
  http.post(`${API}/permissions/:type/:id`, async ({ request }) => {
    const body = (await request.json()) as Record<string, unknown>
    return HttpResponse.json({ id: 'perm-new', ...body }, { status: 201 })
  }),
  http.delete(`${API}/permissions/:type/:id/:principalId`, () =>
    HttpResponse.json({}, { status: 204 }),
  ),

  // ---- Notifications ----------------------------------------------------
  http.get(`${API}/notifications`, () =>
    HttpResponse.json({ items: [], total_count: 0 }),
  ),
  http.get(`${API}/notifications/unread-count`, () => HttpResponse.json({ count: 0 })),
  http.patch(`${API}/notifications/:id/read`, () => HttpResponse.json({}, { status: 204 })),
  http.post(`${API}/notifications/read-all`, () => HttpResponse.json({}, { status: 204 })),

  // ---- Intelligence -----------------------------------------------------
  http.post(`${API}/intelligence/ask`, () =>
    HttpResponse.json({ answer: 'mock answer', sources: [], model: 'test', cost_usd: 0, elapsed_ms: 10 }),
  ),

  // ---- Storage ----------------------------------------------------------
  http.post(`${API}/storage/uploads/initiate`, () =>
    HttpResponse.json({ upload_id: 'up-1', presigned_put_url: 'http://example.test/put', expires_at: '2026-04-17T01:00:00Z' }),
  ),

  // ---- Audit ------------------------------------------------------------
  http.get(`${API}/audit/events`, () => HttpResponse.json({ events: [], page_token: '' })),
]
