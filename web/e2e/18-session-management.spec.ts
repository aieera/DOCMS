// Journey 18 — /settings/sessions management + multi-UA revocation.
//
// Two Playwright browser contexts simulate the same user signed in
// from two "devices" (Chrome on macOS vs Firefox on Windows, different
// UA strings). Context A revokes context B's session via
// DELETE /auth/sessions/{id}. Context B's next GET /auth/me lands on
// a 401 that the interceptor in web/src/api/client.ts translates to a
// forced re-login.
//
// All networking is stubbed — the backend endpoints already exist
// (Blueprint §8.1) but running a real stack for e2e is its own harness.

import { test, expect, type BrowserContext } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'
const SESSION_A = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'
const SESSION_B = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb'

const UA_A = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15'
const UA_B = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:119.0) Gecko/20100101 Firefox/119.0'

test.describe('Journey 18 — session management (multi-UA)', () => {
  test('context A revokes context B; B next request 401s and shows the login modal', async ({ browser }) => {
    // Shared server-side state: which sessions are considered "active".
    // When A revokes B, B's subsequent /auth/me returns 401.
    const active = new Set([SESSION_A, SESSION_B])

    const ctxA = await browser.newContext({ userAgent: UA_A })
    const ctxB = await browser.newContext({ userAgent: UA_B })
    await Promise.all([stubTenant(ctxA, SESSION_A, active), stubTenant(ctxB, SESSION_B, active)])

    const pageA = await ctxA.newPage()
    const pageB = await ctxB.newPage()

    // A opens the sessions page; sees both sessions.
    await pageA.goto('/settings/sessions')
    await expect(pageA.getByTestId('sessions-table')).toBeVisible()
    await expect(pageA.getByTestId(`session-row-${SESSION_A}`)).toBeVisible()
    await expect(pageA.getByTestId(`session-row-${SESSION_A}`)).toContainText('Current session')
    await expect(pageA.getByTestId(`session-row-${SESSION_B}`)).toContainText('Windows')

    // A revokes B. No confirm dialog for single-row revoke.
    await pageA.getByTestId(`revoke-session-${SESSION_B}`).click()
    await expect
      .poll(() => active.has(SESSION_B))
      .toBe(false)

    // B's next authenticated request returns 401 (session gone). The
    // global interceptor redirects to /login on /auth/me 401s.
    await pageB.goto('/dashboard')
    await expect(pageB).toHaveURL(/\/login/)

    await Promise.all([ctxA.close(), ctxB.close()])
  })

  test('revoke all other sessions confirm dialog fires mutation', async ({ browser, page: _page }, testInfo) => {
    const active = new Set([SESSION_A, SESSION_B])
    const ctx = await browser.newContext({ userAgent: UA_A })
    await stubTenant(ctx, SESSION_A, active)
    const page = await ctx.newPage()

    await page.goto('/settings/sessions')
    await page.getByTestId('revoke-all-button').click()
    // ConfirmDialog uses the standard "Revoke all" confirm label.
    await page.getByRole('button', { name: 'Revoke all' }).click()
    await expect
      .poll(() => active.has(SESSION_B))
      .toBe(false)

    // axe clean on the empty-of-other-sessions state.
    await expectAxeClean(page, testInfo, 'settings-sessions-after-revoke-all')
    await ctx.close()
  })
})

// stubTenant wires the /auth/me + /auth/sessions + revoke routes for a
// single browser context. The `active` set is shared between contexts
// so a revoke in A removes a row from B's next read.
async function stubTenant(ctx: BrowserContext, currentID: string, active: Set<string>) {
  await ctx.route('**/api/v1/auth/me', (route) => {
    if (!active.has(currentID)) {
      return route.fulfill({ status: 401, contentType: 'application/json', body: '{}' })
    }
    return route.fulfill({
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
    })
  })

  await ctx.route('**/api/v1/auth/sessions', (route) => {
    const items = [
      session(SESSION_A, UA_A, currentID === SESSION_A),
      session(SESSION_B, UA_B, currentID === SESSION_B),
    ].filter((s) => active.has(s.id))
    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ sessions: items }),
    })
  })

  await ctx.route('**/api/v1/auth/sessions/*', (route) => {
    const m = route.request().url().match(/sessions\/([0-9a-f-]+)$/)
    if (route.request().method() === 'DELETE' && m) {
      active.delete(m[1])
      return route.fulfill({ status: 204, body: '' })
    }
    return route.continue()
  })

  await ctx.route('**/api/v1/auth/sessions/revoke-all', (route) => {
    let n = 0
    for (const id of Array.from(active)) {
      if (id !== currentID) {
        active.delete(id)
        n++
      }
    }
    return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ revoked: n }) })
  })
}

function session(id: string, ua: string, isCurrent: boolean) {
  return {
    id,
    ip_address: id === SESSION_A ? '192.168.1.10' : '10.0.0.5',
    user_agent: ua,
    created_at: new Date(Date.now() - 86_400_000).toISOString(),
    last_activity_at: new Date(Date.now() - 60_000).toISOString(),
    expires_at: new Date(Date.now() + 86_400_000).toISOString(),
    is_current: isCurrent,
  }
}
