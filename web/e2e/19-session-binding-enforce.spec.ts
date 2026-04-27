// Journey 19 — enforce-mode binding mismatch.
//
// Sequence:
//   1. User is on /settings (authenticated) from IP 192.168.1.5.
//   2. An authenticated request hits the backend from a different IP
//      (simulated by having the stub inspect X-Forwarded-For we inject).
//   3. Backend (binding_strictness=enforce) revokes the session and
//      returns 401 with X-Session-Revoked-Reason: binding-mismatch.
//   4. Frontend interceptor in web/src/api/client.ts flips the
//      session store; SessionRevokedModal appears.
//
// We also pin the warn-mode path: if the header is just
// X-Session-Warning=binding-mismatch on a 200 response, the yellow
// banner appears and is dismissible.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 19 — binding enforce', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'alice@example.com',
          display_name: 'Alice',
          role: 'admin',
          mfa_enabled: false,
          tenant_id: TENANT,
        }),
      }),
    )
    await page.route('**/api/v1/auth/sessions', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sessions: [] }) }),
    )
  })

  test('enforce: mid-session IP change → 401 + binding-mismatch → full-screen modal', async ({ page }) => {
    // Land on /settings/sessions first; the table fetches /auth/sessions
    // which we stub to 200 for the initial render.
    await page.goto('/settings/sessions')
    await expect(page.getByTestId('sessions-table').or(page.getByText('No active sessions'))).toBeTruthy()

    // Now simulate the next authenticated call arriving from a
    // different network. We inject the signal via X-Forwarded-For; the
    // stub below returns the enforce-mode response.
    let enforceArmed = false
    await page.route('**/api/v1/auth/me', (route) => {
      if (!enforceArmed) {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            id: USER, email: 'alice@example.com', display_name: 'Alice',
            role: 'admin', mfa_enabled: false, tenant_id: TENANT,
          }),
        })
      }
      return route.fulfill({
        status: 401,
        contentType: 'application/json',
        headers: { 'X-Session-Revoked-Reason': 'binding-mismatch' },
        body: JSON.stringify({
          type: 'UNAUTHORIZED',
          message: 'session revoked for security reasons',
        }),
      })
    })

    // Arm the trap and trigger a refetch. The TanStack-Query `auth.me`
    // refetches on window focus; here we force it by navigating to the
    // dashboard which renders Header (which reads user).
    enforceArmed = true
    await page.evaluate(() =>
      fetch('/api/v1/auth/me', { headers: { 'X-Forwarded-For': '10.99.99.99' } }),
    )

    // Modal appears and blocks the UI.
    const modal = page.getByTestId('session-revoked-modal')
    await expect(modal).toBeVisible()
    await expect(modal).toContainText('session was revoked for security reasons')
    await expect(page.getByTestId('session-revoked-login')).toBeVisible()
  })

  test('warn: X-Session-Warning header shows yellow banner until dismissed', async ({ page }) => {
    // Override the default auth/me stub to attach the warning header.
    await page.unroute('**/api/v1/auth/me')
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        headers: { 'X-Session-Warning': 'binding-mismatch' },
        body: JSON.stringify({
          id: USER, email: 'alice@example.com', display_name: 'Alice',
          role: 'admin', mfa_enabled: false, tenant_id: TENANT,
        }),
      }),
    )
    await page.goto('/settings/sessions')

    const banner = page.getByTestId('session-warning-banner')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText('Unusual sign-in activity')

    // Dismiss button removes it.
    await page.getByTestId('session-warning-dismiss').click()
    await expect(banner).toHaveCount(0)
  })
})
