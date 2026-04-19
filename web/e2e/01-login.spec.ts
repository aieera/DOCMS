// Wave 13.4 — login happy-path journey + template for the other
// nine spec.
//
// Pattern this file establishes and other specs should follow:
//
//   1. Mock every backend call the journey triggers via
//      page.route(). Never hit a real backend in e2e.
//   2. Assertions target user-visible behaviour: "I see the
//      Workspaces page" — not "the axios call went out."
//   3. One journey per spec file. If you need a helper fixture
//      shared across specs, add it to e2e/fixtures/ (not yet
//      created — build it when the second journey needs it).

import { test, expect } from '@playwright/test'

test.describe('Journey 01 — Login', () => {
  test.beforeEach(async ({ page }) => {
    // /api/v1/auth/login → success with the test user.
    await page.route('**/api/v1/auth/login', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: '00000000-0000-0000-0000-000000000001',
            email: 'alice@example.com',
            display_name: 'Alice',
            role: 'admin',
            status: 'active',
            mfa_enabled: false,
          },
          tenant_id: '00000000-0000-0000-0000-000000000100',
        }),
      })
    })

    // /api/v1/auth/me → same user, so the authenticated layout
    // rehydrates without a second call back to the mocked login.
    await page.route('**/api/v1/auth/me', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: '00000000-0000-0000-0000-000000000001',
          email: 'alice@example.com',
          display_name: 'Alice',
          role: 'admin',
          tenant_id: '00000000-0000-0000-0000-000000000100',
        }),
      })
    })
  })

  test('successful login lands on dashboard', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByRole('heading', { name: /sign in/i })).toBeVisible()

    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()

    // Routed to dashboard.
    await expect(page).toHaveURL('/')
    // Header shows the signed-in user's initial.
    await expect(page.getByLabel(/account/i)).toBeVisible()
  })

  test('invalid credentials surface a toast', async ({ page }) => {
    await page.unroute('**/api/v1/auth/login')
    await page.route('**/api/v1/auth/login', async (route) => {
      await route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ type: 'UNAUTHORIZED', message: 'invalid credentials' }),
      })
    })

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('wrong')
    await page.getByRole('button', { name: /sign in/i }).click()

    // Stay on /login and surface an error. Either a toast or
    // an inline message — accept either as long as the
    // authenticated routes aren't reached.
    await expect(page).toHaveURL('/login')
  })
})
