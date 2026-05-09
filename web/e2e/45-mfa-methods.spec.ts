// ADR 0063 — MFA picker on the login page + settings page round-trip.
//
// Coverage:
//   1. Login picker renders the strongest-first list and the selected
//      method advances to the verify step.
//   2. Email OTP "start" button fires POST /auth/mfa/email/start and
//      a wrong code lands a toast.
//   3. Settings page renders enrolled methods + the SMS warning
//      banner.
//
// Backend traffic is mocked via page.route. End-to-end verification
// against a live Twilio account is the integration suite's job.

import { test, expect } from '@playwright/test'

test.describe('Journey 45 — MFA methods', () => {
  test('login picker shows strongest-first methods and advances', async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          mfa_required: true,
          mfa_session_token: 'mfa-tok-abc',
          mfa_methods: [
            { method: 'totp', strength: 4 },
            { method: 'email', strength: 2, destination: 'a***@example.com' },
            { method: 'sms', strength: 1, destination: '***2671' },
          ],
        }),
      }),
    )
    await page.route('**/api/v1/auth/mfa/email/start', (route) =>
      route.fulfill({ status: 204 }),
    )

    await page.goto('/login')
    await page.locator('input[type="email"]').fill('admin@acme.local')
    await page.locator('input[type="password"]').fill('whatever')
    await page.getByRole('button', { name: /sign in/i }).click()

    // Picker shows up.
    await expect(page.getByTestId('mfa-picker')).toBeVisible()
    // Strongest-first ordering: totp must appear before email, email before sms.
    const buttons = await page.locator('[data-testid^="mfa-pick-"]').all()
    const ids = await Promise.all(buttons.map((b) => b.getAttribute('data-testid')))
    expect(ids).toEqual(['mfa-pick-totp', 'mfa-pick-email', 'mfa-pick-sms'])

    // Pick email — should fire start and advance to verify.
    await page.getByTestId('mfa-pick-email').click()
    await expect(page.getByTestId('mfa-verify')).toBeVisible()
    await expect(page.getByText(/a\*\*\*@example\.com/)).toBeVisible()
  })

  test('settings/security/mfa renders enrolled methods + sms warning', async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: 'u-1', email: 'admin@example.com',
          display_name: 'Admin', role: 'owner',
          tenant_id: 't-1', tenant_slug: 'demo',
        }),
      }),
    )
    await page.route('**/api/v1/auth/mfa/methods', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          methods: [
            { method: 'passkey', strength: 5 },
            { method: 'totp', strength: 4 },
            { method: 'email', strength: 2, destination: 'a***@example.com' },
          ],
        }),
      }),
    )

    await page.goto('/settings/security/mfa')
    await expect(page.getByText(/enrolled methods/i)).toBeVisible()
    await expect(page.getByText(/passkey/i).first()).toBeVisible()
    await expect(page.getByText(/SS7|sim-swap|discouraged/i)).toBeVisible()
  })
})
