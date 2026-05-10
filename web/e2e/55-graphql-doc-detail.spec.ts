// ADR 0074 — frontend swap: doc detail page fires the persisted
// DocumentDetail query (single POST /api/v1/graphql) and the
// Activity tab fires ActivityForDocument. The wire shape is the
// APQ extensions envelope (`extensions.persistedQuery.sha256Hash`).
//
// Coverage:
//   1. Loading the doc detail route triggers exactly one POST to
//      /api/v1/graphql with the DocumentDetail hash.
//   2. The Activity tab renders nodes from the GraphQL response
//      (not from any REST call).
//   3. The persisted-query body shape matches what the backend
//      expects (extensions.persistedQuery.sha256Hash + variables).

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER = 'u-1'
const WS = 'w-1'
const DOC = 'doc-1'

const DOCUMENT_DETAIL_HASH =
  '153e448dcdcd66b6713db58f84db62b61fed3a00de0a7a5a93007c8ae3b6e5ea'
const ACTIVITY_HASH =
  'efc2ac8743fbec13e2c0aa8473d226f9b8c74b4e52a4eca6367cd4dc9086d4ef'

interface GraphQLPost {
  operationName?: string
  variables?: Record<string, unknown>
  extensions?: { persistedQuery?: { version: number; sha256Hash: string } }
}

const restStubs = async (page: import('@playwright/test').Page) => {
  await page.route('**/api/v1/auth/me', (r) =>
    r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        id: USER, email: 'me@example.com', display_name: 'Me',
        role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
      }),
    }))
  await page.route('**/api/v1/tasks/mine**', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))

  // The REST useDocument hook still fires for header sidecar info.
  await page.route(`**/api/v1/documents/${DOC}`, (r) =>
    r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        id: DOC,
        tenant_id: TENANT,
        workspace_id: WS,
        title: 'Acme Q1 Report.pdf',
        description: '',
        lifecycle_state: 'LIFECYCLE_STATE_ACTIVE',
        region_pin: 'EU_WEST_1',
        tags: [],
        mime_type: 'application/pdf',
        size_bytes: 12345,
        version_count: 1,
        current_version_id: 'v-1',
        created_by: USER,
        created_by_name: 'Me',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      }),
    }))

  // OCR endpoint — needed by the failure banner.
  await page.route(`**/api/v1/ocr/${DOC}/v-1`, (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'completed', pages: [], avg_confidence: 0.99 }) }))
}

test.describe('Journey 55 — GraphQL doc detail', () => {
  test.beforeEach(async ({ page }) => {
    await restStubs(page)
  })

  test('doc detail fires a single DocumentDetail GraphQL call', async ({ page }) => {
    let detailCalls = 0
    let lastBody: GraphQLPost | null = null

    await page.route('**/api/v1/graphql', async (route) => {
      const body = JSON.parse(route.request().postData() ?? '{}') as GraphQLPost
      lastBody = body
      const hash = body.extensions?.persistedQuery?.sha256Hash
      if (hash === DOCUMENT_DETAIL_HASH) {
        detailCalls++
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            data: {
              document: {
                id: DOC, tenantId: TENANT, workspaceId: WS,
                title: 'Acme Q1 Report.pdf',
                lifecycleState: 'ACTIVE', regionPin: 'EU_WEST_1', tags: [],
                mimeType: 'application/pdf', totalSizeBytes: 12345,
                createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
                currentVersion: { id: 'v-1', versionNumber: 1, sizeBytes: 12345, createdAt: new Date().toISOString(), createdByName: 'Me' },
                versions: { nodes: [{ id: 'v-1', versionNumber: 1, createdAt: new Date().toISOString() }], nextCursor: '' },
                comments: { nodes: [], nextCursor: '' },
                annotations: { nodes: [], nextCursor: '' },
                workflowInstances: [],
                permissions: { canView: true, canEdit: true, canDelete: true, canShare: true, canAdmin: true },
              },
            },
          }),
        })
        return
      }
      // Unknown hash — assert the rejection path matches the
      // backend's PersistedQueryNotFound contract.
      await route.fulfill({
        status: 400, contentType: 'application/json',
        body: JSON.stringify({ error: 'PersistedQueryNotFound', hash }),
      })
    })

    await page.goto(`/workspaces/${WS}/documents/${DOC}`)
    await expect(page.locator('h1', { hasText: 'Acme Q1 Report.pdf' })).toBeVisible()

    expect(detailCalls).toBe(1)
    expect(lastBody?.operationName).toBe('DocumentDetail')
    expect(lastBody?.variables).toEqual({ id: DOC })
    expect(lastBody?.extensions?.persistedQuery?.sha256Hash).toBe(DOCUMENT_DETAIL_HASH)
  })

  test('activity tab fires ActivityForDocument and renders nodes', async ({ page }) => {
    let activityCalls = 0
    await page.route('**/api/v1/graphql', async (route) => {
      const body = JSON.parse(route.request().postData() ?? '{}') as GraphQLPost
      const hash = body.extensions?.persistedQuery?.sha256Hash
      if (hash === DOCUMENT_DETAIL_HASH) {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            data: {
              document: {
                id: DOC, tenantId: TENANT, workspaceId: WS,
                title: 'Acme Q1 Report.pdf',
                lifecycleState: 'ACTIVE', regionPin: 'EU_WEST_1', tags: [],
                createdAt: new Date().toISOString(),
                currentVersion: { id: 'v-1', versionNumber: 1, createdAt: new Date().toISOString() },
                versions: { nodes: [], nextCursor: '' },
                comments: { nodes: [], nextCursor: '' },
                annotations: { nodes: [], nextCursor: '' },
                workflowInstances: [],
                permissions: { canView: true, canEdit: false, canDelete: false, canShare: false, canAdmin: false },
              },
            },
          }),
        })
        return
      }
      if (hash === ACTIVITY_HASH) {
        activityCalls++
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            data: {
              activityForDocument: {
                nodes: [
                  { id: 'a-1', kind: 'document.create', actorName: 'Me', summary: 'document.create', occurredAt: new Date().toISOString() },
                  { id: 'a-2', kind: 'comment.posted', actorName: 'Me', summary: 'comment.posted', occurredAt: new Date().toISOString() },
                ],
                nextCursor: '',
              },
            },
          }),
        })
        return
      }
      await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'PersistedQueryNotFound', hash }) })
    })

    await page.goto(`/workspaces/${WS}/documents/${DOC}`)
    await expect(page.locator('h1', { hasText: 'Acme Q1 Report.pdf' })).toBeVisible()

    // Click the Activity tab. The label is in the TABS const so it
    // renders as a button by data-testid scheme used by other panels.
    await page.getByRole('button', { name: 'Activity' }).click()
    await expect(page.getByTestId('activity-feed')).toBeVisible()
    await expect(page.getByTestId('activity-feed').locator('li')).toHaveCount(2)
    expect(activityCalls).toBe(1)
  })
})
