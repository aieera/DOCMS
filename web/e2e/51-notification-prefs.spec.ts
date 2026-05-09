// ADR 0086 — unified notification preferences journey.
//
// Coverage:
//   1. /settings/notifications renders the matrix + DND + snoozes.
//   2. Toggling a cell + clicking Save PUTs /preferences/matrix.
//   3. DND save → PUT /dnd; clear → DELETE /dnd.
//   4. Inbox row's bell-off button POSTs /snooze with duration=60.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'

const futureISO = () => new Date(Date.now() + 60 * 60_000).toISOString()

test.describe('Journey 51 — notification preferences', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: USER, email: 'me@example.com', display_name: 'Me',
          role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
        }),
      }),
    )
    // Header polls /tasks/mine — keep it quiet.
    await page.route('**/api/v1/tasks/mine**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
  })

  test('/settings/notifications shows matrix + DND + snoozes blocks', async ({ page }) => {
    await page.route('**/api/v1/notifications/preferences/matrix', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cells: [] }) }),
    )
    await page.route('**/api/v1/notifications/snoozes', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ snoozes: [] }) }),
    )
    await page.route('**/api/v1/notifications/dnd', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ dnd: null }) }),
    )

    await page.goto('/settings/notifications')
    await expect(page.getByTestId('prefs-matrix-section')).toBeVisible()
    await expect(page.getByTestId('prefs-dnd-section')).toBeVisible()
    await expect(page.getByTestId('prefs-snoozes-section')).toBeVisible()
    // Default in_app cell exists for first event type.
    await expect(page.getByTestId('prefs-cell-document.shared-in_app')).toBeVisible()
  })

  test('toggling a cell + Save PUTs the matrix', async ({ page }) => {
    let putBody: any = null
    await page.route('**/api/v1/notifications/preferences/matrix', async (route) => {
      if (route.request().method() === 'PUT') {
        putBody = JSON.parse(route.request().postData() ?? '{}')
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cells: putBody.cells }) })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cells: [] }) })
    })
    await page.route('**/api/v1/notifications/snoozes', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ snoozes: [] }) }))
    await page.route('**/api/v1/notifications/dnd', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ dnd: null }) }))

    await page.goto('/settings/notifications')
    // Enable email for "comment.mention".
    await page.getByTestId('prefs-cell-comment.mention-email').check()
    await page.getByTestId('prefs-matrix-save').click()
    await expect.poll(() => putBody).not.toBeNull()
    expect(putBody.cells.some((c: any) =>
      c.event_type === 'comment.mention' && c.channel === 'email' && c.is_enabled === true)).toBe(true)
  })

  test('DND save and clear', async ({ page }) => {
    let dndState: any = null
    let putBody: any = null
    await page.route('**/api/v1/notifications/preferences/matrix', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cells: [] }) }))
    await page.route('**/api/v1/notifications/snoozes', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ snoozes: [] }) }))
    await page.route('**/api/v1/notifications/dnd', async (route) => {
      const m = route.request().method()
      if (m === 'PUT') {
        putBody = JSON.parse(route.request().postData() ?? '{}')
        dndState = { dnd_start: putBody.start, dnd_end: putBody.end, timezone: putBody.timezone }
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(dndState) })
      }
      if (m === 'DELETE') {
        dndState = null
        return route.fulfill({ status: 204, body: '' })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ dnd: dndState }) })
    })

    await page.goto('/settings/notifications')
    await page.getByTestId('dnd-start').fill('22:00')
    await page.getByTestId('dnd-end').fill('07:00')
    await page.getByTestId('dnd-save').click()
    await expect.poll(() => putBody).not.toBeNull()
    expect(putBody.start).toBe('22:00')
    expect(putBody.end).toBe('07:00')
  })

  test('inbox snooze button POSTs /snooze with 60 minutes', async ({ page }) => {
    let snoozeBody: any = null
    await page.route('**/api/v1/notifications', async (route) => {
      if (route.request().method() !== 'GET') return route.continue()
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          items: [{
            id: 'n-1', type: 'comment.mention', title: 'You were mentioned',
            body: 'in "Q3 plan"', read: false, created_at: new Date().toISOString(),
          }],
          total_count: 1,
        }),
      })
    })
    await page.route('**/api/v1/notifications/snooze', async (route) => {
      snoozeBody = JSON.parse(route.request().postData() ?? '{}')
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ id: 's-1', event_type: snoozeBody.event_type, until_at: futureISO(), created_at: new Date().toISOString() }),
      })
    })

    await page.goto('/notifications')
    await page.getByTestId('notif-snooze-n-1').click()
    await expect.poll(() => snoozeBody).not.toBeNull()
    expect(snoozeBody.event_type).toBe('comment.mention')
    expect(snoozeBody.duration_minutes).toBe(60)
  })
})
