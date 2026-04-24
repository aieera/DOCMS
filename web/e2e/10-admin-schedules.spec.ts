// Journey 10 — admin schedules panel.
//
// Wave 15.4 added `SignatureProfileOrphanSweeper` as a daily 03:00 UTC
// Temporal schedule per tenant. The admin panel at
// /admin/platform/schedules surfaces it alongside the existing
// password-expiry + ack-reminders schedules.
//
// This spec stubs GET /api/v1/platform/schedules and asserts:
//   - the three known schedule sections render
//   - the signature-profile-orphan-sweep row is present and healthy
//     (num_missed = 0, next_run visible, last_run visible)
//   - axe-clean on serious + critical
//
// NB: filename intentionally uses slot 10 per the prompt. The existing
// 10-audit-export.spec.ts has been renamed to 10a-audit-export to
// avoid a Playwright ID collision; see the rename commit.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

// Three tenants' worth of schedules so the section-grouping code gets
// exercised. Each section appears once in the header count.
const SCHEDULES = [
  // Password expiry — one healthy, one with a missed catchup.
  { id: `password-expiry-${TENANT}`, paused: false, next_run: iso(3600), last_run: iso(-86400), num_actions: 42, num_missed: 0, running_count: 0 },
  { id: 'password-expiry-00000000-0000-0000-0000-000000000101', paused: false, next_run: iso(3600), last_run: iso(-86400), num_actions: 39, num_missed: 2, running_count: 0 },
  // Ack reminders.
  { id: `ack-reminders-${TENANT}`, paused: false, next_run: iso(7200), last_run: iso(-86400), num_actions: 17, num_missed: 0, running_count: 0 },
  // The Wave-15.4 orphan sweeper — the row the spec explicitly asserts on.
  { id: `signature-profile-orphan-sweep-${TENANT}`, paused: false, next_run: iso(10800), last_run: iso(-86400), num_actions: 5, num_missed: 0, running_count: 0 },
]

function iso(offsetSeconds: number): string {
  return new Date(Date.now() + offsetSeconds * 1000).toISOString()
}

test.describe('Journey 10 — admin schedules panel', () => {
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
    await page.route('**/api/v1/platform/schedules', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ schedules: SCHEDULES }),
      }),
    )
  })

  test('renders the signature-profile-orphan-sweep row as healthy and is axe-clean', async ({ page }, testInfo) => {
    await page.goto('/admin/platform/schedules')

    // Three sections headers render — Password Expiry, Ack Reminders,
    // Signature Profile Orphan Sweeper.
    await expect(page.getByTestId('admin-schedules')).toBeVisible()
    await expect(page.getByTestId('schedules-section-Password Expiry')).toBeVisible()
    await expect(page.getByTestId('schedules-section-Acknowledgement Reminders')).toBeVisible()
    await expect(page.getByTestId('schedules-section-Signature Profile Orphan Sweeper')).toBeVisible()

    // The Wave 15.4 row is present with zero missed (healthy) and a
    // visible next/last run — this is the regression guard the prompt
    // asked for.
    const rowId = `signature-profile-orphan-sweep-${TENANT}`
    const row = page.getByTestId(`schedule-row-${rowId}`)
    await expect(row).toBeVisible()
    await expect(page.getByTestId(`schedule-missed-${rowId}`)).toHaveText('0')
    // Actions count: 5 (see the fixture above).
    await expect(page.getByTestId(`schedule-actions-${rowId}`)).toHaveText('5')

    await expectAxeClean(page, testInfo, 'admin-schedules')
  })
})
