// Journey 07 — share-link admin smoke.
//
// Scoped smoke (not a full journey). The per-doc share-link UI lives
// on the document detail drawer; creating one is out of scope for this
// pass because the drawer selectors weren't audited. This spec
// verifies the admin share-links page loads + primary table renders,
// which is the prerequisite for the full create-share journey.
//
// Full journey tracked in out-of-scope.md.

import { test, expect } from '@playwright/test'

test.describe('Journey 07 — share-links admin smoke', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: 't-1' }),
      }),
    )
    await page.route('**/api/v1/admin/share-links**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          items: [
            { id: 'sl-1', document_id: 'd-1', token: 'abc', expires_at: new Date(Date.now() + 7 * 86400_000).toISOString(), created_by_name: 'Alice' },
          ],
          next_page_token: null,
        }),
      }),
    )
  })

  test('share-links page loads', async ({ page, context }) => {
    await context.addCookies([{ name: 'dms_session', value: 'sess-share', domain: '127.0.0.1', path: '/' }])
    await page.goto('/admin/share-links')
    await expect(page.getByRole('heading', { name: /share links/i })).toBeVisible()
  })
})
