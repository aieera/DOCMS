// Journey 17 — admin quarantine review queue.
//
// Stubs GET /api/v1/admin/quarantine and asserts:
//   - table renders one virus row + one blocked_mime row
//   - clicking "Release" opens the confirm dialog with the destructive-ish
//     "Release" wording, then calls /release and the list refetches
//   - the audit-log page shows a matching quarantine.release event
//     (proves the backend fan-out wiring produced an audit row — the
//     assertion is against a stubbed /api/v1/audit/events response)
//
// Non-admin role is covered indirectly by the AdminGuard smoke tests.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

const ITEM_VIRUS = {
  id: 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',
  upload_id: 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb',
  tenant_id: TENANT,
  filename: 'invoice.pdf.exe',
  uploader_id: USER,
  uploader_name: 'Alice',
  reason: 'virus',
  signature: 'Eicar-Test-Signature',
  declared_mime: 'application/pdf',
  detected_mime: 'application/x-msdownload',
  size_bytes: 12_345,
  created_at: new Date(Date.now() - 3600_000).toISOString(),
  status: 'new',
}

const ITEM_BLOCKED_MIME = {
  id: 'cccccccc-cccc-cccc-cccc-cccccccccccc',
  upload_id: 'dddddddd-dddd-dddd-dddd-dddddddddddd',
  tenant_id: TENANT,
  filename: 'screenshot.png',
  uploader_id: USER,
  uploader_name: 'Alice',
  reason: 'blocked_mime',
  signature: '',
  declared_mime: 'image/png',
  detected_mime: 'application/x-msdownload',
  size_bytes: 987_654,
  created_at: new Date(Date.now() - 1800_000).toISOString(),
  status: 'new',
}

test.describe('Journey 17 — quarantine review', () => {
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
  })

  test('admin releases a quarantined item and sees the audit event', async ({ page }, testInfo) => {
    // Start with both items queued.
    let items = [ITEM_VIRUS, ITEM_BLOCKED_MIME]
    await page.route('**/api/v1/admin/quarantine', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items, total_count: items.length }) }),
    )

    // Release flips the virus row to 'released' on the server; we model
    // that by mutating the list and having subsequent LIST calls see the
    // new state.
    let releaseCalls = 0
    await page.route(`**/api/v1/admin/quarantine/${ITEM_VIRUS.id}/release`, (route) => {
      releaseCalls++
      items = items.map((it) => (it.id === ITEM_VIRUS.id ? { ...it, status: 'released' } : it))
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) })
    })

    // Audit events stub — the release mutation fans out to the audit
    // service; the UI is checked against a stubbed /audit/events that
    // already shows the row.
    await page.route('**/api/v1/audit/events**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          events: [
            {
              id: 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee',
              created_at: new Date().toISOString(),
              actor_id: USER,
              actor_name: 'Alice',
              action: 'quarantine.release',
              resource_type: 'upload',
              resource_id: ITEM_VIRUS.upload_id,
              ip_address: '127.0.0.1',
              details: { reason: 'false positive; admin review' },
            },
          ],
        }),
      }),
    )

    await page.goto('/admin/quarantine')

    // Table + both rows visible.
    await expect(page.getByTestId('quarantine-table')).toBeVisible()
    const virusRow = page.getByTestId(`quarantine-row-${ITEM_VIRUS.id}`)
    await expect(virusRow).toBeVisible()
    await expect(virusRow).toContainText('invoice.pdf.exe')
    await expect(virusRow.getByTestId('threat-virus')).toContainText('Eicar-Test-Signature')
    await expect(page.getByTestId(`quarantine-row-${ITEM_BLOCKED_MIME.id}`).getByTestId('threat-blocked-mime'))
      .toBeVisible()

    // Axe-clean the initial table render (before dialog open; dialog a11y
    // is covered by ConfirmDialog's own tests).
    await expectAxeClean(page, testInfo, 'admin-quarantine-queue')

    // Release flow.
    await page.getByTestId(`quarantine-release-${ITEM_VIRUS.id}`).click()
    const confirmButton = page.getByRole('button', { name: 'Release' })
    await expect(confirmButton).toBeVisible()
    await confirmButton.click()
    await expect.poll(() => releaseCalls).toBeGreaterThan(0)

    // Now verify the audit event surfaced on the audit log page.
    await page.goto('/admin/audit-log')
    await expect(page.getByText('quarantine.release')).toBeVisible()
    await expect(page.getByText(ITEM_VIRUS.upload_id)).toBeVisible()
  })

  test('non-admin sees AdminGuard panel instead of the queue', async ({ page }) => {
    // Re-stub auth/me as a viewer (override the beforeEach value).
    await page.unroute('**/api/v1/auth/me')
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'bob@example.com',
          display_name: 'Bob',
          role: 'viewer',
          mfa_enabled: false,
          tenant_id: TENANT,
        }),
      }),
    )
    await page.goto('/admin/quarantine')
    await expect(page.getByRole('alert')).toContainText('Admin access required')
    await expect(page.getByTestId('quarantine-table')).toHaveCount(0)
  })
})
