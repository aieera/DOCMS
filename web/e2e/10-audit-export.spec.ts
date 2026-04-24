// Journey 10 — audit-log CSV export.
// admin/audit-log → Export CSV button → browser sees a download that
// the mocked backend produces.

import { test, expect } from '@playwright/test'

test.describe('Journey 10 — audit-log export', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: 't-1' }),
      }),
    )
    await page.route('**/api/v1/audit**', (route) => {
      // Listing vs export — same prefix; diff on Accept or path tail.
      if (route.request().url().includes('export')) {
        return route.fulfill({
          status: 200,
          headers: { 'Content-Type': 'text/csv', 'Content-Disposition': 'attachment; filename="audit.csv"' },
          body: 'id,actor,action,resource_id\nevt-1,u-1,login,null\n',
        })
      }
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ items: [{ id: 'evt-1', actor: 'u-1', action: 'login' }] }),
      })
    })
  })

  test('export button triggers a CSV download', async ({ page, context }) => {
    await context.addCookies([{ name: 'dms_session', value: 'sess-audit', domain: '127.0.0.1', path: '/' }])
    await page.goto('/admin/audit-log')
    await expect(page.getByRole('heading', { name: /audit log/i })).toBeVisible()

    // audit-log.tsx gives the button data-testid="audit-export"; use
    // that because the label has an icon that breaks accessible-name
    // matching on some locales.
    const downloadPromise = page.waitForEvent('download')
    await page.getByTestId('audit-export').click()
    const download = await downloadPromise
    expect(download.suggestedFilename()).toContain('.csv')
  })
})
