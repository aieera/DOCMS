import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import {
  getUsers,
  inviteUser,
  suspendUser,
  resetMFA,
  getAuditLog,
  getTenantSettings,
  updateTenantSettings,
} from '@/api/admin'

describe('api/admin', () => {
  it('getUsers() adapts backend {users,next_cursor} to {items,total_count,page_token}', async () => {
    // The frontend expects PaginatedResponse<User>. The backend (auth
    // service remediation 04b) returns {users, next_cursor}. The adapter
    // in api/admin.ts is the glue — pin its contract here.
    server.use(
      http.get('*/api/v1/admin/users', () =>
        HttpResponse.json({
          users: [
            { id: '1', email: 'a@a.com' },
            { id: '2', email: 'b@b.com' },
          ],
          next_cursor: 'cursor-xyz',
        }),
      ),
    )
    const got = await getUsers()
    expect(got.items).toHaveLength(2)
    expect(got.total_count).toBe(2)
    expect(got.page_token).toBe('cursor-xyz')
  })

  it('getUsers() handles a backend response with no users field gracefully', async () => {
    server.use(
      http.get('*/api/v1/admin/users', () => HttpResponse.json({})),
    )
    const got = await getUsers()
    expect(got.items).toEqual([])
    expect(got.total_count).toBe(0)
  })

  it('getUsers() forwards query params', async () => {
    let url = ''
    server.use(
      http.get('*/api/v1/admin/users', ({ request }) => {
        url = request.url
        return HttpResponse.json({ users: [], next_cursor: '' })
      }),
    )
    await getUsers({ status: 'suspended', q: 'alice' })
    expect(url).toContain('status=suspended')
    expect(url).toContain('q=alice')
  })

  it('inviteUser() sends display_name + role', async () => {
    let body: unknown = null
    server.use(
      http.post('*/api/v1/admin/users/invite', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({ invite_token: 't' }, { status: 201 })
      }),
    )
    await inviteUser('alice@acme.local', 'member', 'Alice')
    expect(body).toMatchObject({
      email: 'alice@acme.local',
      role: 'member',
      display_name: 'Alice',
    })
  })

  it('suspendUser() POSTs to the user-scoped suspend URL', async () => {
    let url = ''
    server.use(
      http.post('*/api/v1/admin/users/:id/suspend', ({ request }) => {
        url = request.url
        return HttpResponse.json({}, { status: 204 })
      }),
    )
    await suspendUser('user-123')
    expect(url).toContain('/admin/users/user-123/suspend')
  })

  it('resetMFA() POSTs to the user-scoped reset-mfa URL', async () => {
    let url = ''
    server.use(
      http.post('*/api/v1/admin/users/:id/reset-mfa', ({ request }) => {
        url = request.url
        return HttpResponse.json({}, { status: 204 })
      }),
    )
    await resetMFA('user-xyz')
    expect(url).toContain('/admin/users/user-xyz/reset-mfa')
  })

  it('getAuditLog() hits /audit/events (fixed from legacy /admin/audit-log)', async () => {
    let url = ''
    server.use(
      http.get('*/api/v1/audit/events', ({ request }) => {
        url = request.url
        return HttpResponse.json({ events: [] })
      }),
    )
    await getAuditLog()
    expect(url).toContain('/audit/events')
  })

  it('getTenantSettings() + updateTenantSettings() round-trip feature flags', async () => {
    const flags = await getTenantSettings()
    expect(flags).toHaveProperty('ai_enabled')
    const out = await updateTenantSettings({ ai_enabled: false })
    expect(out).toMatchObject({ ai_enabled: false })
  })
})
