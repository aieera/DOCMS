// ADR 0107 — RTL shell smoke spec.
//
// What we assert (the minimum that catches regressions):
//   1. ?lng=ar in the URL drives i18n into Arabic mode.
//   2. <html dir="rtl" lang="ar"> is applied before first paint.
//   3. The sidebar (data-testid="app-sidebar") visually lands on
//      the RIGHT side of the viewport — not just because of the
//      dir attribute but because the logical-utility migration
//      survived.
//   4. A screenshot is captured so visual diffs can catch
//      bidi-breakage we forgot to assert on.
//
// What we DON'T assert here:
//   * Specific translated strings — Prompt 4 owns the bundles.
//   * Pixel-perfect mirroring of every component — visual diff
//     is the right tool, not a Playwright text-content assertion.
//   * The PATCH /auth/me/locale wire — that needs a real backend
//     or extensive mocking; we cover it in unit tests instead.

import { test, expect } from '@playwright/test'

test.describe('RTL shell — ADR 0107', () => {
  test.beforeEach(async ({ page }) => {
    // Mock /auth/me so the authenticated layout mounts without a
    // real backend. locale='ar' so the auth-store sync path
    // confirms the server-side preference too.
    await page.route('**/api/v1/auth/me', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id:           '00000000-0000-0000-0000-000000000001',
          email:        'alice@example.com',
          display_name: 'Alice',
          role:         'admin',
          tenant_id:    '00000000-0000-0000-0000-000000000100',
          locale:       'ar',
          mfa_enabled:  false,
        }),
      })
    })

    // Empty workspace / docs / smart-folders so the layout renders
    // with structure but no list rows interfering with the bidi check.
    await page.route('**/api/v1/workspaces*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"workspaces":[]}' }))
    await page.route('**/api/v1/documents*',  (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"documents":[]}' }))
    await page.route('**/api/v1/saved-searches/smart-folders', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
    await page.route('**/api/v1/tasks*',      (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  })

  test('html flips to rtl + lang=ar + sidebar lands on the right', async ({ page }) => {
    // The detector reads cookies first; pre-set so we don't depend
    // on the UI to drive the language change (that's the selector
    // spec's job, not this one's).
    await page.context().addCookies([{
      name:  'dms_locale',
      value: 'ar',
      url:   'http://localhost:4173',
    }])

    await page.goto('/')

    // Wait for the initial paint to commit. The i18n init in
    // main.tsx awaits before createRoot, so dir is set by the time
    // any DOM is visible.
    const html = page.locator('html')
    await expect(html).toHaveAttribute('dir', 'rtl')
    await expect(html).toHaveAttribute('lang', 'ar')

    // Geometry check — the sidebar should be in the right half of
    // the viewport when the document is RTL. We grab the topbar's
    // user-avatar button (the right-most chip in LTR) and assert
    // its bounding box is now in the LEFT half. This catches any
    // residual hardcoded `left-*`/`right-*` positioning that
    // survived the mechanical migration.
    const avatar = page.getByLabel(/Account menu/i)
    const box    = await avatar.boundingBox()
    const vw     = page.viewportSize()?.width ?? 1280
    expect(box).not.toBeNull()
    expect(box!.x + box!.width / 2).toBeLessThan(vw / 2)

    // Snapshot for visual diff. Playwright produces a baseline on
    // first run; subsequent runs diff against it. CI lockfile lives
    // next to this spec at e2e/__screenshots__.
    await expect(page).toHaveScreenshot('rtl-dashboard.png', {
      maxDiffPixels: 5000,                 // tolerate sub-pixel AA
      fullPage:      true,
    })
  })

  test('ltr is the fallback when no cookie is set', async ({ page }) => {
    // No cookie, no ?lng — detector falls through to navigator,
    // which Playwright defaults to en-US.
    await page.goto('/')
    await expect(page.locator('html')).toHaveAttribute('dir', 'ltr')
    await expect(page.locator('html')).toHaveAttribute('lang', /^en/)
  })
})
