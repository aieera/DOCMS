// Journey 15 — internal-auth admin panel (ADR 0031 observability).
//
// Two slices:
//   (a) /admin/platform/internal-auth renders 12 service cards with
//       success/rejected counts, mode badges, and cert-expiry tone.
//   (b) The trusted-proxy dry-run tester round-trips an XFF chain and
//       shows the resolved client IP plus a per-hop trust breakdown.
//
// All backend endpoints are stubbed — no Prometheus container, no
// workflow service running. We mock /platform/metrics/query per
// PromQL and /platform/trusted-proxy/test for the dry-run.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

const SERVICES = [
  'auth', 'policy', 'document', 'storage', 'search', 'audit',
  'workflow', 'notification', 'signature', 'billing', 'connector',
  'acknowledgement',
]

// promVector builds a Prometheus "vector" response with one sample
// per service. Second element of `value` is always a string to match
// the wire format.
function promVector(job_to_value: Record<string, string>) {
  return {
    status: 'success',
    data: {
      resultType: 'vector',
      result: Object.entries(job_to_value).map(([job, v]) => ({
        metric: { job },
        value: [Date.now() / 1000, v],
      })),
    },
  }
}

function promModeVector(job_to_mode: Record<string, string>) {
  return {
    status: 'success',
    data: {
      resultType: 'vector',
      result: Object.entries(job_to_mode).map(([job, mode]) => ({
        metric: { job, mode },
        value: [Date.now() / 1000, '1'],
      })),
    },
  }
}

test.describe('Journey 15 — internal-auth admin panel', () => {
  test.beforeEach(async ({ page }) => {
    // Admin session. The route's AdminGuard reads role off useAuthStore
    // — we hydrate it by returning the user on /auth/me and letting
    // the existing _authenticated.beforeLoad call it during route
    // resolution.
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

    // PromQL passthrough — route by the query string inside the body
    // so each of the four panel queries gets the right fixture.
    await page.route('**/api/v1/platform/metrics/query', async (route, request) => {
      const body = request.postDataJSON?.() as { query?: string } | undefined
      const q = body?.query ?? ''
      let upstream
      if (q.includes('outcome="ok"')) {
        upstream = promVector(Object.fromEntries(SERVICES.map((s) => [s, '42'])))
      } else if (q.includes('outcome!="ok"')) {
        upstream = promVector(Object.fromEntries(SERVICES.map((s) => [s, '0'])))
      } else if (q.includes('internal_auth_mode_info')) {
        upstream = promModeVector(Object.fromEntries(SERVICES.map((s) => [s, 'both'])))
      } else if (q.includes('cert_expiry_timestamp_seconds')) {
        // 15 days out — amber tone.
        const t = Math.floor(Date.now() / 1000) + 15 * 86400
        upstream = promVector(Object.fromEntries(SERVICES.map((s) => [s, String(t)])))
      } else {
        upstream = { status: 'success', data: { resultType: 'vector', result: [] } }
      }
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ upstream }),
      })
    })
  })

  test('grid renders 12 service cards and is axe-clean', async ({ page }, testInfo) => {
    await page.goto('/admin/platform/internal-auth')

    // The 12 cards render once the four parallel queries resolve.
    await expect(page.getByTestId('internal-auth-service-grid')).toBeVisible()
    for (const s of SERVICES) {
      await expect(page.getByTestId(`service-card-${s}`)).toBeVisible()
    }

    // Success count 42 is rendered for at least the first service.
    await expect(page.getByTestId('success-auth')).toHaveText('42')
    // Rejected count 0.
    await expect(page.getByTestId('rejected-auth')).toHaveText('0')

    await expectAxeClean(page, testInfo, 'internal-auth-health')
  })

  test('trusted-proxy dry-run resolves client IP from backend', async ({ page }) => {
    await page.route('**/api/v1/platform/trusted-proxy/test', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          resolved_ip: '198.51.100.9',
          peer: { addr: '10.0.0.5', trusted: true, parseable: true },
          hops: [
            { addr: '198.51.100.9', trusted: false, parseable: true },
            { addr: '10.0.0.4', trusted: true, parseable: true },
          ],
          trusted_cidrs: ['10.0.0.0/8'],
        }),
      }),
    )

    await page.goto('/admin/platform/internal-auth')
    await expect(page.getByTestId('trusted-proxy-panel')).toBeVisible()

    // Default sample values are prefilled; submitting fires the mock.
    await page.getByTestId('trusted-proxy-run').click()

    await expect(page.getByTestId('trusted-proxy-resolved')).toHaveText('198.51.100.9')
    // Trusted CIDR echoed back into the read-only list.
    await expect(page.getByTestId('trusted-proxy-cidrs')).toContainText('10.0.0.0/8')
  })
})
