// Journey 10c — security-posture admin widget (ADR 0033).
//
// Two slices, one spec:
//   (a) stub a failing posture → banner appears on every admin route.
//   (b) the /admin/platform/security page renders 3 cards + secret
//       scan footer + deep-link.
//
// Stubs hit /api/v1/platform/security/posture directly; the audit
// aggregator is upstream of this in production and out of frontend
// test scope.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

function iso(offsetSeconds: number) {
  return new Date(Date.now() + offsetSeconds * 1000).toISOString()
}

const FAILING_POSTURE = {
  any_failing: true,
  scans: [
    {
      scan_type: 'sast',
      status: 'fail',
      critical_count: 2,
      high_count: 5,
      run_id: '1234567890',
      run_url: 'https://github.com/aieera/DOCMS/actions/runs/1234567890',
      ran_at: iso(-3600),
    },
    {
      scan_type: 'dep_scan',
      status: 'pass',
      critical_count: 0,
      high_count: 0,
      run_id: '1234567891',
      run_url: 'https://github.com/aieera/DOCMS/actions/runs/1234567891',
      ran_at: iso(-3600),
    },
    {
      scan_type: 'dast',
      status: 'unknown',
      critical_count: 0,
      high_count: 0,
    },
    {
      scan_type: 'secret_scan',
      status: 'pass',
      critical_count: 0,
      high_count: 0,
      run_id: '1234567893',
      run_url: 'https://github.com/aieera/DOCMS/actions/runs/1234567893',
      ran_at: iso(-1800),
    },
  ],
}

const CLEAN_POSTURE = {
  any_failing: false,
  scans: [
    { scan_type: 'sast', status: 'pass', critical_count: 0, high_count: 0, ran_at: iso(-3600) },
    { scan_type: 'dep_scan', status: 'pass', critical_count: 0, high_count: 0, ran_at: iso(-3600) },
    { scan_type: 'dast', status: 'pass', critical_count: 0, high_count: 0, ran_at: iso(-3600) },
    { scan_type: 'secret_scan', status: 'pass', critical_count: 0, high_count: 0, ran_at: iso(-1800) },
  ],
}

test.describe('Journey 10c — security posture admin widget', () => {
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

  test('failing posture renders banner on admin index', async ({ page }) => {
    await page.route('**/api/v1/platform/security/posture', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(FAILING_POSTURE),
      }),
    )
    await page.goto('/admin')

    // Banner shows up and names the failing gate so the operator
    // doesn't have to drill in to know which one broke.
    const banner = page.getByTestId('security-banner')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText('Security CI gate failing')
    await expect(banner).toContainText('SAST')

    // Link into the detail page is present and correct.
    await expect(page.getByTestId('security-banner-link')).toHaveAttribute(
      'href',
      /\/admin\/platform\/security/,
    )
  })

  test('clean posture hides the banner', async ({ page }) => {
    await page.route('**/api/v1/platform/security/posture', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(CLEAN_POSTURE),
      }),
    )
    await page.goto('/admin')
    await expect(page.getByTestId('security-banner')).toHaveCount(0)
  })

  test('/admin/platform/security renders 3 cards + footer + axe-clean', async ({
    page,
  }, testInfo) => {
    await page.route('**/api/v1/platform/security/posture', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify(FAILING_POSTURE),
      }),
    )
    await page.goto('/admin/platform/security')

    // Three cards render.
    await expect(page.getByTestId('security-card-sast')).toBeVisible()
    await expect(page.getByTestId('security-card-dep_scan')).toBeVisible()
    await expect(page.getByTestId('security-card-dast')).toBeVisible()

    // SAST card shows fail status + counters.
    await expect(page.getByTestId('security-card-sast-status')).toContainText('fail')
    await expect(page.getByTestId('security-card-sast-critical')).toHaveText('2')
    await expect(page.getByTestId('security-card-sast-high')).toHaveText('5')
    await expect(page.getByTestId('security-card-sast-link')).toHaveAttribute(
      'href',
      /github\.com\/aieera\/DOCMS\/actions\/runs\/1234567890/,
    )

    // DAST card is "unknown" (backend placeholder for never-run).
    await expect(page.getByTestId('security-card-dast-status')).toContainText('unknown')

    // Secret-scan lives in the footer, not as a card.
    await expect(page.getByTestId('security-footer-secret')).toContainText('Secret scan')

    await expectAxeClean(page, testInfo, 'admin-security-posture')
  })
})
