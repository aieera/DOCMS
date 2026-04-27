// Journey 20 — region-pin violation surfaces as a 451 toast.
//
// Flow:
//   1. User is on /admin/compliance (authenticated admin).
//   2. Backend stub returns 451 + error body { type: "REGION_VIOLATION" }
//      for any residency-scoped mutation. This is the same shape
//      pkg/errors/ToHTTPError emits when the document service's
//      resolveRegionForCreate rejects an out-of-allowlist region.
//   3. The global interceptor in web/src/api/client.ts renders
//      "Operation blocked: data residency rule violated…" as a
//      react-hot-toast error.
//
// Also pins the RegionPinBadge tooltip contract: the EU badge
// advertises the GDPR framework, MENA advertises PDPL, etc. A silent
// regression in BOUNDARY_COPY (e.g. EU mis-tagged as US) would read
// here first.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 20 — region pinning', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'owner@example.com',
          display_name: 'Owner',
          role: 'owner',
          mfa_enabled: false,
          tenant_id: TENANT,
        }),
      }),
    )
    await page.route('**/api/v1/residency/stats', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { region: 'eu-west-1', doc_count: 1200, blob_bytes: 5_000_000_000 },
          { region: 'me-south-1', doc_count: 340, blob_bytes: 1_200_000_000 },
        ]),
      }),
    )
  })

  test('451 REGION_VIOLATION renders residency-specific toast', async ({ page }) => {
    // Any mutation on a residency-scoped endpoint returns the new 451 shape.
    await page.route('**/api/v1/residency/migrations', (route) => {
      if (route.request().method() === 'POST') {
        return route.fulfill({
          status: 451,
          contentType: 'application/json',
          body: JSON.stringify({
            type: 'REGION_VIOLATION',
            message: 'region "us-east-1" not allowed: not in organization\'s allowed_regions',
            details: { region: 'us-east-1' },
          }),
        })
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      })
    })

    await page.goto('/admin/residency')

    // Trigger the POST — the /admin/residency page exposes a migration
    // submit. We fire it directly via fetch to avoid coupling this
    // spec to the exact form-markup evolution.
    await page.evaluate(async () => {
      try {
        await fetch('/api/v1/residency/migrations', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ source_region: 'eu-west-1', target_region: 'us-east-1' }),
        })
      } catch {
        // axios throws on 4xx; the interceptor fires regardless.
      }
    })

    // react-hot-toast renders into a live region; assert the copy.
    const toast = page.getByText(/data residency rule violated/i).first()
    await expect(toast).toBeVisible()
  })

  test('ResidencyComplianceCard renders per-region breakdown with RegionPinBadge', async ({ page }) => {
    await page.goto('/admin/compliance')
    const card = page.getByTestId('residency-compliance-card')
    await expect(card).toBeVisible()
    await expect(page.getByTestId('residency-compliance-pct')).toContainText('100%')

    // Each region row renders its badge with the expected boundary
    // label in the tooltip (title attr).
    const eu = page.getByTestId('region-pin-badge-eu-west-1').first()
    await expect(eu).toBeVisible()
    await expect(eu).toHaveAttribute('title', /GDPR/)
    await expect(eu).toHaveAttribute('data-boundary', 'EU')

    const mena = page.getByTestId('region-pin-badge-me-south-1').first()
    await expect(mena).toHaveAttribute('data-boundary', 'MENA')
    await expect(mena).toHaveAttribute('title', /PDPL/)
  })
})
