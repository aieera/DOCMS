// Journey 04 — MFA recovery code login.
// TOTP step → "Use a recovery code" link → recovery form → dashboard
// with the re-enroll warning toast.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 04 — MFA recovery', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ mfa_required: true, mfa_session_token: 'mfa-sess-bbb' }),
      }),
    )
    await page.route('**/api/v1/auth/mfa/recovery', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          user: { id: USER, email: 'alice@example.com', display_name: 'Alice', role: 'admin', status: 'active', tenant_id: TENANT },
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: USER, email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: TENANT }),
      }),
    )
  })

  test('recovery code path lands on dashboard and surfaces re-enroll toast', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
    await expect(page.getByText(/enter your authenticator code/i)).toBeVisible()

    await page.getByRole('button', { name: /use a recovery code/i }).click()
    await expect(page.getByText(/enter a recovery code/i)).toBeVisible()

    await page.getByLabel(/recovery code/i).fill('CODE-ABCD-1234')
    await page.getByRole('button', { name: /sign in/i }).click()

    await expect(page).toHaveURL('/')
    // The login flow toasts a reminder that the user must re-enroll
    // MFA. Match on a stable substring rather than the exact wording.
    await expect(page.getByText(/re-enroll mfa/i)).toBeVisible({ timeout: 3_000 })
  })
})
