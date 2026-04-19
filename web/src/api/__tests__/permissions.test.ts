import { describe, it, expect } from 'vitest'
import { http, HttpResponse } from 'msw'
import { server } from '@/test/mocks/server'
import {
  getPermissions,
  checkPermission,
  grantPermission,
  revokePermission,
  type Permission,
} from '@/api/permissions'

const DOC = '11111111-1111-1111-1111-111111111111'
const UID = '22222222-2222-2222-2222-222222222222'

describe('api/permissions', () => {
  it('getPermissions() GETs the resource ACL', async () => {
    server.use(
      http.get('*/api/v1/permissions/document/:id', () =>
        HttpResponse.json<Permission[]>([
          {
            id: 'p1',
            resource_type: 'document',
            resource_id: DOC,
            principal_type: 'user',
            principal_id: UID,
            capability: 'view',
            granted_by: UID,
            granted_at: '2026-04-17T00:00:00Z',
          },
        ]),
      ),
    )
    const list = await getPermissions('document', DOC)
    expect(list).toHaveLength(1)
    expect(list[0].capability).toBe('view')
  })

  it('checkPermission() returns the boolean from the backend', async () => {
    server.use(
      http.post('*/api/v1/permissions/check', () =>
        HttpResponse.json({ allowed: true, reason: '' }),
      ),
    )
    const ok = await checkPermission('view', 'document', DOC)
    expect(ok).toBe(true)
  })

  it('checkPermission() body shape matches backend contract', async () => {
    let captured: unknown = null
    server.use(
      http.post('*/api/v1/permissions/check', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ allowed: false, reason: 'policy denied' })
      }),
    )
    await checkPermission('edit', 'folder', 'fold-1')
    // Backend expects snake_case fields — this test catches accidental
    // renames to camelCase.
    expect(captured).toMatchObject({ action: 'edit', resource_type: 'folder', resource_id: 'fold-1' })
  })

  it('grantPermission() POSTs body with snake_case field names', async () => {
    let captured: unknown = null
    server.use(
      http.post('*/api/v1/permissions/document/:id', async ({ request }) => {
        captured = await request.json()
        return HttpResponse.json({ id: 'p-new' }, { status: 201 })
      }),
    )
    await grantPermission('document', DOC, 'user', UID, 'view')
    expect(captured).toMatchObject({
      principal_type: 'user',
      principal_id: UID,
      capability: 'view',
    })
  })

  it('revokePermission() DELETEs the right URL with principalId', async () => {
    let urlSeen = ''
    server.use(
      http.delete('*/api/v1/permissions/:type/:id/:principalId', ({ request }) => {
        urlSeen = request.url
        return HttpResponse.json({}, { status: 204 })
      }),
    )
    await revokePermission('document', DOC, UID)
    expect(urlSeen).toContain(`/permissions/document/${DOC}/${UID}`)
  })
})
