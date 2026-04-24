// Journey 03 — MFA TOTP challenge.
// Login returns mfa_required → TOTP form renders → 6-digit verify → dashboard.
//
// Matches the 3-step state machine in web/src/routes/login.tsx.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 03 — MFA TOTP', () => {
  test.beforeEach(async ({ page }) => {
    // Login returns mfa_required=true + an mfa_session_token instead of
    // a finished session. The login page should route to the TOTP step.
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          mfa_required: true,
          mfa_session_token: 'mfa-sess-aaa',
        }),
      }),
    )
    // MFA verify returns the real session.
    await page.route('**/api/v1/auth/mfa/verify', (route) =>
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

  test('valid TOTP lands on dashboard', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()

    // TOTP step visible.
    await expect(page.getByText(/enter your authenticator code/i)).toBeVisible()
    await page.getByLabel(/6-digit code/i).fill('123456')
    await page.getByRole('button', { name: /verify/i }).click()

    await expect(page).toHaveURL('/')
  })
})
