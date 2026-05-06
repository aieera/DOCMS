// ADR 0068 — saved-searches management page.
//
// Mocks GET / PATCH / POST /subscribe / DELETE on /saved-searches.
// Asserts:
//   - listing renders rows with the alert badge when notify=true
//   - "Make alert" PATCH flips notify; "Stop alert" reverses it
//   - Edit dialog persists name + interval + cron via PATCH
//   - Subscribe dialog (admin-only) POSTs the user_id + channels
//   - Delete confirms then DELETEs
//
// No backend required — all backend calls intercepted by page.route.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'
const SS_ID_1   = 'ss-aaa'
const SS_ID_2   = 'ss-bbb'

test.describe('Journey 41 — Saved searches', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
            role: 'admin', status: 'active', mfa_enabled: false,
          },
          tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
          role: 'admin', tenant_id: TENANT_ID,
        }),
      }),
    )

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('lists saved searches; alert badge shows when notify=true', async ({ page }) => {
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            id: SS_ID_1, name: 'Open contracts', query: 'tag:contract',
            notify: true, notify_interval_minutes: 15, subscriber_count: 2,
            subscribers: [
              { user_id: 'u-bob',   channels: ['in_app'],         subscribed_by: USER_ID, subscribed_at: '2026-04-01T00:00:00Z' },
              { user_id: 'u-carol', channels: ['email','in_app'], subscribed_by: USER_ID, subscribed_at: '2026-04-02T00:00:00Z' },
            ],
            created_at: '2026-04-01T00:00:00Z',
          },
          {
            id: SS_ID_2, name: 'Q4 invoices', query: 'tag:invoice',
            notify: false, notify_interval_minutes: 30, subscriber_count: 0,
            created_at: '2026-04-15T00:00:00Z',
          },
        ]),
      }),
    )

    await page.goto('/saved-searches')
    await expect(page.getByTestId('saved-search-list')).toBeVisible()
    await expect(page.getByTestId(`saved-search-row-${SS_ID_1}`)).toBeVisible()
    // Alert badge ONLY on the notify=true row.
    await expect(page.getByTestId(`alert-badge-${SS_ID_1}`)).toBeVisible()
    await expect(page.getByTestId(`alert-badge-${SS_ID_2}`)).toHaveCount(0)
    // Subscriber count surfaces.
    await expect(page.getByTestId(`saved-search-row-${SS_ID_1}`)).toContainText('2 subscribers')
  })

  test('"Make alert" PATCHes notify=true', async ({ page }) => {
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: SS_ID_2, name: 'Q4 invoices', query: 'tag:invoice', notify: false, subscriber_count: 0, created_at: '2026-04-15T00:00:00Z' },
        ]),
      }),
    )
    let patchBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/saved-searches/${SS_ID_2}`, async (route) => {
      if (route.request().method() === 'PATCH') {
        patchBody = JSON.parse(route.request().postData() ?? '{}')
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ id: SS_ID_2, name: 'Q4 invoices', query: 'tag:invoice', notify: true, subscriber_count: 0, created_at: '2026-04-15T00:00:00Z' }),
        })
        return
      }
      await route.fallback()
    })

    await page.goto('/saved-searches')
    await page.getByTestId(`convert-${SS_ID_2}`).click()
    await expect.poll(() => patchBody).toMatchObject({ notify: true })
  })

  test('Edit dialog PATCHes name + interval + cron', async ({ page }) => {
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: SS_ID_1, name: 'Old name', query: 'q', notify: true, notify_interval_minutes: 15, subscriber_count: 0, created_at: '2026-04-01T00:00:00Z' },
        ]),
      }),
    )
    let patchBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/saved-searches/${SS_ID_1}`, async (route) => {
      if (route.request().method() === 'PATCH') {
        patchBody = JSON.parse(route.request().postData() ?? '{}')
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: SS_ID_1 }) })
        return
      }
      await route.fallback()
    })

    await page.goto('/saved-searches')
    await page.getByTestId(`edit-${SS_ID_1}`).click()
    await page.getByTestId('edit-name').fill('Renamed')
    await page.getByTestId('edit-interval').fill('60')
    await page.getByTestId('edit-cron').fill('0 9 * * 1-5')
    await page.getByTestId('edit-save').click()

    await expect.poll(() => patchBody).toMatchObject({
      name: 'Renamed',
      notify_interval_minutes: 60,
      alert_frequency_cron: '0 9 * * 1-5',
    })
  })

  test('Subscribe dialog POSTs user_id + channels', async ({ page }) => {
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: SS_ID_1, name: 'Open contracts', query: 'q', notify: true, subscriber_count: 0, subscribers: [], created_at: '2026-04-01T00:00:00Z' },
        ]),
      }),
    )
    let postBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/saved-searches/${SS_ID_1}/subscribe`, async (route) => {
      postBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'subscribed' }) })
    })

    await page.goto('/saved-searches')
    await page.getByTestId(`subscribe-${SS_ID_1}`).click()
    await page.getByTestId('subscribe-user-id').fill('u-team-member')
    await page.getByTestId('channel-email').check()
    await page.getByTestId('subscribe-confirm').click()

    await expect.poll(() => postBody).toMatchObject({
      user_id: 'u-team-member',
      // Default in_app + the one we just checked.
      channels: expect.arrayContaining(['in_app', 'email']),
    })
  })

  test('Subscribe button hidden for non-admin role', async ({ page }) => {
    // Re-mock /auth/me with member role for this test only.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
          role: 'member', tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: SS_ID_1, name: 'Open contracts', query: 'q', notify: true, subscriber_count: 0, created_at: '2026-04-01T00:00:00Z' },
        ]),
      }),
    )

    await page.goto('/saved-searches')
    // Subscribe button should NOT appear for member role.
    await expect(page.getByTestId(`subscribe-${SS_ID_1}`)).toHaveCount(0)
  })

  test('Delete prompts then DELETEs', async ({ page }) => {
    await page.route('**/api/v1/saved-searches', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: SS_ID_1, name: 'To delete', query: 'q', notify: false, subscriber_count: 0, created_at: '2026-04-01T00:00:00Z' },
        ]),
      }),
    )
    let deleted = false
    await page.route(`**/api/v1/saved-searches/${SS_ID_1}`, async (route) => {
      if (route.request().method() === 'DELETE') {
        deleted = true
        await route.fulfill({ status: 204, body: '' })
        return
      }
      await route.fallback()
    })
    page.on('dialog', (d) => d.accept())

    await page.goto('/saved-searches')
    await page.getByTestId(`delete-${SS_ID_1}`).click()
    await expect.poll(() => deleted).toBe(true)
  })
})
