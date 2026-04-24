// Journey 12 — Wave 15.2 geofence admin + deny/step-up surfacing.
//
// Covers two user-facing slices:
//   (a) /admin/geofences list + create + dry-run tester.
//   (b) Any request returning 451 or 428 triggers the client-side
//       interceptor toasts (surfaced via the interceptor wired in
//       web/src/api/client.ts).

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 12 — geofence admin', () => {
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
          tenant_id: TENANT,
        }),
      }),
    )
  })

  test('list + dry-run deny decision', async ({ page }) => {
    await page.route('**/api/v1/admin/geofences', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          geofences: [
            {
              id: 'gf-1',
              tenant_id: TENANT,
              scope: 'tenant',
              mode: 'deny',
              country_codes: ['CN'],
              apply_to: '*',
              enabled: true,
              created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            },
          ],
        }),
      }),
    )
    await page.route('**/api/v1/admin/geofences/test', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          allow: false,
          require_step_up: false,
          reason: 'country_deny',
          matched_policy_id: 'gf-1',
        }),
      }),
    )

    await page.goto('/admin/geofences')
    await expect(page.getByText(/deny/i).first()).toBeVisible()
    await expect(page.getByText(/Countries:\s*CN/i)).toBeVisible()

    // Dry-run tester.
    await page.getByLabel(/test an ip/i).fill('203.0.113.5')
    await page.getByRole('button', { name: /evaluate/i }).click()
    await expect(page.getByText(/DENY/)).toBeVisible()
    await expect(page.getByText(/country_deny/)).toBeVisible()
  })

  test('451 from a downstream request surfaces the geofence toast', async ({ page }) => {
    await page.route('**/api/v1/admin/geofences', (route) =>
      route.fulfill({
        status: 451,
        contentType: 'application/json',
        headers: { 'x-geofence-reason': 'country_deny' },
        body: '',
      }),
    )
    await page.goto('/admin/geofences')
    await expect(page.getByText(/Request blocked by geofence policy/i)).toBeVisible()
  })

  test('428 surfaces the step-up toast', async ({ page }) => {
    await page.route('**/api/v1/admin/geofences', (route) =>
      route.fulfill({
        status: 428,
        contentType: 'application/json',
        headers: { 'www-authenticate': 'Step-Up' },
        body: '',
      }),
    )
    await page.goto('/admin/geofences')
    await expect(page.getByText(/additional verification required/i)).toBeVisible()
  })

  test('/admin/geofences has no axe serious/critical violations', async ({ page }, testInfo) => {
    await page.route('**/api/v1/admin/geofences', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ geofences: [] }),
      }),
    )
    await page.goto('/admin/geofences')
    await expectAxeClean(page, testInfo, 'admin-geofences')
  })
})
