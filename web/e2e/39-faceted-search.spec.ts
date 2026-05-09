// ADR 0082 — /search facet sidebar journey. Mocks /search and
// /saved-searches; asserts URL state, sidebar interaction, bookmark
// roundtrip, and saved-filter set hydration.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 39 — Faceted search', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
            role: 'admin', status: 'active', mfa_enabled: false,
          },
          tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
          role: 'admin', tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/saved-searches', (route) => {
      if (route.request().method() === 'GET') {
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([]),
        })
      } else {
        route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
      }
    })

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('renders facet sidebar with bucket counts', async ({ page }) => {
    await page.route('**/api/v1/search', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [{
            document_id: 'd1', title: 'Acme MSA', mime_type: 'application/pdf',
            workspace_id: 'w1', size_bytes: 102400, lifecycle_state: 'active',
            tags: ['contract'], created_by_name: 'Alice',
            created_at: '2026-04-01T00:00:00Z', score: 1.5, has_thumbnail: false,
          }],
          total_count: 1, latency_ms: 12, search_mode: 'lexical',
          facets: {
            tag:            [{ value: 'contract', count: 12 }, { value: 'invoice', count: 8 }],
            author:         [{ value: 'Alice', count: 7 }, { value: 'Bob', count: 4 }],
            classification: [{ value: 'msa', count: 5 }],
          },
        }),
      }),
    )

    await page.goto('/search?q=contract')
    await expect(page.getByTestId('facet-sidebar')).toBeVisible()

    // Each requested facet renders as a collapsible group with counts.
    await expect(page.getByTestId('facet-group-tag')).toContainText('contract')
    await expect(page.getByTestId('facet-group-tag')).toContainText('12')
    await expect(page.getByTestId('facet-group-author')).toContainText('Alice')
    await expect(page.getByTestId('facet-group-author')).toContainText('7')
  })

  test('checking a facet bucket pushes the value into the URL', async ({ page }) => {
    let lastBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/search', (route) => {
      lastBody = JSON.parse(route.request().postData() ?? '{}')
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [], total_count: 0, latency_ms: 5, search_mode: 'lexical',
          facets: {
            tag:    [{ value: 'urgent', count: 3 }, { value: 'legal', count: 2 }],
            author: [{ value: 'Alice', count: 7 }],
          },
        }),
      })
    })

    await page.goto('/search?q=contract')
    await page.getByTestId('facet-tag-urgent').check()

    // URL gets the new repeated param.
    await expect(page).toHaveURL(/[?&]tag=urgent\b/)

    // The next /search request body carries the filter.
    await expect.poll(() =>
      ((lastBody?.filters as Record<string, unknown>)?.tags as string[])?.includes('urgent'),
    ).toBe(true)

    // Toggling again clears it.
    await page.getByTestId('facet-tag-urgent').uncheck()
    await expect(page).not.toHaveURL(/[?&]tag=urgent\b/)
  })

  test('bookmarked URL hydrates the sidebar selection', async ({ page }) => {
    await page.route('**/api/v1/search', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [], total_count: 0, latency_ms: 5, search_mode: 'lexical',
          facets: {
            tag:    [{ value: 'urgent', count: 3 }, { value: 'legal', count: 2 }],
            author: [{ value: 'Alice', count: 7 }],
          },
        }),
      }),
    )

    // Land directly on the URL the previous test produced — sidebar
    // checkbox must come up checked, and the active-filter chip in
    // the header should show the count.
    await page.goto('/search?q=contract&tag=urgent&tag=legal')
    await expect(page.getByTestId('facet-tag-urgent')).toBeChecked()
    await expect(page.getByTestId('facet-tag-legal')).toBeChecked()
    await expect(page.getByTestId('clear-filters')).toContainText('2')
  })

  test('saved filter set posts the filter blob and re-hydrates on apply', async ({ page }) => {
    let postedSave: Record<string, unknown> | null = null
    await page.route('**/api/v1/search', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [], total_count: 0, latency_ms: 5, search_mode: 'lexical',
          facets: { tag: [{ value: 'urgent', count: 3 }] },
        }),
      }),
    )

    let savedList: Array<{ id: string; name: string; query: string; filters: Record<string, unknown> }> = []
    await page.route('**/api/v1/saved-searches', async (route) => {
      const req = route.request()
      if (req.method() === 'GET') {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify(savedList),
        })
        return
      }
      // POST
      postedSave = JSON.parse(req.postData() ?? '{}')
      const created = {
        id: 'ss-1',
        name: (postedSave as { name: string }).name,
        query: (postedSave as { query: string }).query,
        filters: (postedSave as { filters: Record<string, unknown> }).filters,
        created_at: new Date().toISOString(),
      }
      savedList = [created]
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify(created),
      })
    })

    page.on('dialog', async (dialog) => {
      // The page uses prompt() to ask for the saved-search name.
      await dialog.accept('open contracts')
    })

    await page.goto('/search?q=contract&tag=urgent')
    await page.getByTestId('save-search').click()

    // POST body carries query + filters.tags=['urgent'].
    await expect.poll(() => postedSave).toMatchObject({
      name: 'open contracts',
      query: 'contract',
      filters: { tags: ['urgent'] },
    })

    // The saved-list refetch picks up the new entry; clicking the
    // chip re-hydrates the URL.
    await page.goto('/search')
    await page.getByTestId('apply-saved-open contracts').click()
    await expect(page).toHaveURL(/q=contract/)
    await expect(page).toHaveURL(/tag=urgent/)
  })

  test('Clear button drops every filter except q', async ({ page }) => {
    await page.route('**/api/v1/search', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [], total_count: 0, latency_ms: 5, search_mode: 'lexical',
          facets: { tag: [{ value: 'urgent', count: 3 }] },
        }),
      }),
    )

    await page.goto('/search?q=contract&tag=urgent&author=Alice')
    await expect(page.getByTestId('clear-filters')).toBeVisible()
    await page.getByTestId('clear-filters').click()

    // Only q remains; tag + author are gone.
    await expect(page).toHaveURL(/q=contract/)
    await expect(page).not.toHaveURL(/tag=/)
    await expect(page).not.toHaveURL(/author=/)
  })
})
