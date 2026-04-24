// Journey 09 — admin invite user.
// Log in as admin → /admin/users → "Invite user" button → fill form →
// POST succeeds → row appears in the table.

import { test, expect } from '@playwright/test'

test.describe('Journey 09 — admin invite user', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: 't-1' }),
      }),
    )

    // Users list: return an empty list first, then include the newly
    // invited user after POST. Tracking via a counter rather than
    // server-side state keeps the mock simple.
    let userListCalls = 0
    await page.route('**/api/v1/admin/users**', (route) => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          status: 201, contentType: 'application/json',
          body: JSON.stringify({ id: 'u-new', email: 'bob@example.com', display_name: 'Bob', role: 'member', status: 'invited' }),
        })
      }
      userListCalls++
      const items = userListCalls === 1
        ? [{ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', status: 'active' }]
        : [
            { id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', status: 'active' },
            { id: 'u-new', email: 'bob@example.com', display_name: 'Bob', role: 'member', status: 'invited' },
          ]
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ items, next_page_token: null }),
      })
    })
  })

  test('invite user button posts + new row renders', async ({ page, context }) => {
    await context.addCookies([{ name: 'dms_session', value: 'sess-admin-users', domain: '127.0.0.1', path: '/' }])
    await page.goto('/admin/users')
    await expect(page.getByRole('heading', { name: /users/i })).toBeVisible()

    // users.tsx uses aria-label="Invite user" on the primary button.
    await page.getByRole('button', { name: /invite user/i }).click()

    // Form renders in a dialog. Use role-based selectors that match
    // the Radix Dialog + Input components.
    await page.getByLabel(/email/i).fill('bob@example.com')
    await page.getByLabel(/display name/i).fill('Bob')
    await page.getByRole('button', { name: /send invite|invite/i }).click()

    // Post-invite list shows the new row.
    await expect(page.getByText('bob@example.com')).toBeVisible({ timeout: 3_000 })
  })
})
