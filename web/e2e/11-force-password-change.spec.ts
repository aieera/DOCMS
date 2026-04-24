// Journey 11 — Wave 15.3 force-change-password.
//
// Login returns require_password_change=true + a one-time token →
// page redirects to /change-password?token=... → user enters a new
// password satisfying the policy → POST /auth/change-password
// returns a session → dashboard.
//
// The strength-meter checklist is the a11y surface the brief
// requires (DoD: "strength meter, checklist, show/hide toggle").

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 11 — force-change-password', () => {
  test.beforeEach(async ({ page }) => {
    // Login hands back a change token instead of a session.
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          require_password_change: true,
          one_time_change_token: 'pwchange-tok-aaa',
        }),
      }),
    )
    // Change-password returns the real session.
    await page.route('**/api/v1/auth/change-password', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER,
            email: 'alice@example.com',
            display_name: 'Alice',
            role: 'admin',
            status: 'active',
            tenant_id: TENANT,
          },
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'alice@example.com',
          display_name: 'Alice',
          role: 'admin',
          tenant_id: TENANT,
        }),
      }),
    )
  })

  test('policy-compliant password lands on dashboard', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('old-password-doesnt-matter')
    await page.getByRole('button', { name: /sign in/i }).click()

    // Landed on /change-password with token in query.
    await expect(page).toHaveURL(/\/change-password/)
    await expect(page.getByRole('heading', { name: /set a new password/i })).toBeVisible()

    // Requirements-checklist heuristic: when all checks pass the
    // submit button becomes enabled. Pick a password that hits each.
    const pw = 'CorrectHorse1!'
    const newPw = page.getByLabel(/new password/i)
    const confirm = page.getByLabel(/confirm new password/i)
    await newPw.fill(pw)
    await confirm.fill(pw)

    await page.getByRole('button', { name: /update password/i }).click()
    await expect(page).toHaveURL('/')
  })

  test('non-matching confirm keeps submit disabled', async ({ page }) => {
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('whatever')
    await page.getByRole('button', { name: /sign in/i }).click()
    await expect(page).toHaveURL(/\/change-password/)

    await page.getByLabel(/new password/i).fill('CorrectHorse1!')
    await page.getByLabel(/confirm new password/i).fill('differenT2@')

    const submit = page.getByRole('button', { name: /update password/i })
    await expect(submit).toBeDisabled()
  })

  test('change-password page has no axe serious/critical violations', async ({ page }, testInfo) => {
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('whatever')
    await page.getByRole('button', { name: /sign in/i }).click()
    await expect(page).toHaveURL(/\/change-password/)

    await expectAxeClean(page, testInfo, 'change-password')
  })
})
