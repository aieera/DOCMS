// Journey 06 — search.
// Log in → go to /search → type query → results render → save search.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 06 — search', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: USER, email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: TENANT }),
      }),
    )
    await page.route('**/api/v1/search**', (route) => {
      const url = new URL(route.request().url())
      const q = url.searchParams.get('query') || ''
      if (q.length < 2) {
        return route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ results: [], total_count: 0, latency_ms: 0, search_mode: 'lexical' }),
        })
      }
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          results: [
            { id: 'd1', title: 'Acme contract', snippet: 'Acme agreement — CONFIDENTIAL', score: 0.9 },
            { id: 'd2', title: 'Q3 invoice', snippet: 'Invoice to Acme', score: 0.7 },
          ],
          total_count: 2, latency_ms: 42, search_mode: 'lexical',
        }),
      })
    })
    await page.route('**/api/v1/search/saved', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ items: [] }),
      }),
    )
  })

  test('typing query renders results and save-search button is enabled', async ({ page, context }) => {
    await context.addCookies([{ name: 'dms_session', value: 'sess-search', domain: '127.0.0.1', path: '/' }])
    await page.goto('/search')
    await page.getByRole('searchbox').fill('acme')
    await expect(page.getByText('Acme contract')).toBeVisible()
    await expect(page.getByText('Q3 invoice')).toBeVisible()
    // "Save search" button wires a data-testid per the admin
    // convention so this is stable across UI tweaks.
    await expect(page.getByTestId('save-search')).toBeEnabled()
  })
})
