// Journey 08 — API key rotate/revoke smoke.
//
// Scoped smoke. The "rotate" flow in the brief maps to `revoke +
// create` in this UI — api-keys.tsx exposes a Revoke button with
// aria-label="Revoke" and a Create form. Full create-rotate-revoke
// journey is tracked in out-of-scope.md; this asserts the admin
// page loads, listing is shown, and the Revoke control is reachable.

import { test, expect } from '@playwright/test'

test.describe('Journey 08 — API keys admin smoke', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: 't-1' }),
      }),
    )
    await page.route('**/api/v1/admin/api-keys**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          items: [
            { key_id: 'ak-1', name: 'ci', created_at: new Date(Date.now() - 86400_000).toISOString(), last_used_at: null },
          ],
        }),
      }),
    )
  })

  test('api-keys page renders with a revoke button', async ({ page, context }) => {
    await context.addCookies([{ name: 'dms_session', value: 'sess-apikeys', domain: '127.0.0.1', path: '/' }])
    await page.goto('/admin/api-keys')
    await expect(page.getByRole('heading', { name: /api keys/i })).toBeVisible()
    // api-keys.tsx renders aria-label="Revoke" on the per-row action.
    await expect(page.getByRole('button', { name: /^revoke$/i }).first()).toBeVisible()
  })
})
