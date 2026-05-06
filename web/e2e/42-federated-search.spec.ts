// ADR 0069 — platform-admin federated search journey.
//
// Verifies:
//   - red banner is visible (load-bearing visual gate)
//   - submit disabled until reason ≥10 chars + non-empty query
//   - 403 from server surfaces "not platform admin" toast
//   - successful search renders results grouped by tenant
//   - audit log embed lists the row after a query

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 42 — Cross-tenant support search', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'support@example.com', display_name: 'Support',
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
          id: USER_ID, email: 'support@example.com', display_name: 'Support',
          role: 'admin', tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/platform/search/federated/audit**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      }),
    )

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('support@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
    await page.waitForURL((url) => !url.pathname.includes('/login'))
  })

  test('cross-tenant banner is visible — load-bearing visual gate', async ({ page }) => {
    await page.goto('/admin/platform/support-search')
    const banner = page.getByTestId('cross-tenant-banner')
    await expect(banner).toBeVisible()
    await expect(banner).toContainText(/BYPASSES tenant isolation/i)
    await expect(banner).toContainText(/100 queries/i)
  })

  test('submit disabled until reason ≥10 chars AND query non-empty', async ({ page }) => {
    await page.goto('/admin/platform/support-search')
    const submit = page.getByTestId('federated-submit')
    await expect(submit).toBeDisabled()

    // Just a query — still disabled (no reason)
    await page.getByTestId('federated-query').fill('test query')
    await expect(submit).toBeDisabled()

    // Reason too short
    await page.getByTestId('federated-reason').fill('test')
    await expect(submit).toBeDisabled()
    await expect(page.getByText(/at least 10 characters/i)).toBeVisible()

    // Both gates met — enabled.
    await page.getByTestId('federated-reason').fill('Incident SUP-1234 — checking phishing payload propagation')
    await expect(submit).toBeEnabled()
  })

  test('successful search renders results_by_tenant', async ({ page }) => {
    let postBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/platform/search/federated', async (route) => {
      postBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results_by_tenant: {
            'tenant-A': [
              { document_id: 'd1', title: 'Acme MSA',     workspace_id: 'w1', size_bytes: 1024, mime_type: 'application/pdf', created_at: '2026-04-01T00:00:00Z', lifecycle_state: 'active', score: 9.5 },
              { document_id: 'd2', title: 'Acme Invoice', workspace_id: 'w1', size_bytes: 2048, mime_type: 'application/pdf', created_at: '2026-04-02T00:00:00Z', lifecycle_state: 'active', score: 7.3 },
            ],
            'tenant-B': [
              { document_id: 'd3', title: 'Other doc',    workspace_id: 'w2', size_bytes: 4096, mime_type: 'application/pdf', created_at: '2026-04-03T00:00:00Z', lifecycle_state: 'active', score: 6.1 },
            ],
          },
          total_hits: 3,
          tenants_with_hits: 2,
          audit_id: '00000000-0000-0000-0000-000000000abc',
          latency_ms: 87,
        }),
      })
    })

    await page.goto('/admin/platform/support-search')
    await page.getByTestId('federated-reason').fill('Incident SUP-1234 — checking phishing payload propagation')
    await page.getByTestId('federated-query').fill('Acme phish')
    await page.getByTestId('federated-submit').click()

    await expect(page.getByTestId('federated-results')).toBeVisible()
    await expect(page.getByTestId('tenant-bucket-tenant-A')).toContainText('Acme MSA')
    await expect(page.getByTestId('tenant-bucket-tenant-A')).toContainText('Acme Invoice')
    await expect(page.getByTestId('tenant-bucket-tenant-B')).toContainText('Other doc')

    // Body carried the reason verbatim — the load-bearing audit input.
    await expect.poll(() => postBody).toMatchObject({
      query:  'Acme phish',
      reason: 'Incident SUP-1234 — checking phishing payload propagation',
    })
  })

  test('403 from server surfaces a clear "not platform admin" toast', async ({ page }) => {
    await page.route('**/api/v1/platform/search/federated', (route) =>
      route.fulfill({
        status: 403,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'platform.search.federated permission required' }),
      }),
    )

    await page.goto('/admin/platform/support-search')
    await page.getByTestId('federated-reason').fill('Incident SUP-1234 — verifying access scope')
    await page.getByTestId('federated-query').fill('test')
    await page.getByTestId('federated-submit').click()

    await expect(page.getByText(/not a platform admin/i)).toBeVisible()
    // Audit list re-fetches even on denial — the row was written
    // server-side with outcome=denied_perm.
  })

  test('429 quota response surfaces dedicated toast', async ({ page }) => {
    await page.route('**/api/v1/platform/search/federated', (route) =>
      route.fulfill({
        status: 429,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'daily federated query limit reached (100/day)' }),
      }),
    )

    await page.goto('/admin/platform/support-search')
    await page.getByTestId('federated-reason').fill('Incident SUP-9999 — final probe of the day')
    await page.getByTestId('federated-query').fill('x')
    await page.getByTestId('federated-submit').click()

    await expect(page.getByText(/Daily limit reached/i)).toBeVisible()
  })

  test('audit list renders rows after fetch including DENIED outcomes', async ({ page }) => {
    await page.route('**/api/v1/platform/search/federated/audit**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            id: 'audit-1',
            reason: 'incident SUP-1',
            query_payload: { query: 'phishing-hash' },
            results_summary: { total_hits: 2, tenants_with_hits: 1 },
            latency_ms: 87,
            outcome: 'success',
            created_at: '2026-05-06T18:00:00Z',
          },
          {
            id: 'audit-2',
            reason: 'incident SUP-2',
            query_payload: { query: 'too-many-today' },
            results_summary: {},
            latency_ms: 5,
            outcome: 'denied_quota',
            created_at: '2026-05-06T18:30:00Z',
          },
        ]),
      }),
    )

    await page.goto('/admin/platform/support-search')
    await expect(page.getByTestId('audit-list')).toBeVisible()
    await expect(page.getByTestId('audit-row-audit-1')).toContainText('phishing-hash')
    // Denied row must be visible — compliance review needs to see attempts.
    await expect(page.getByTestId('audit-row-audit-2')).toContainText('denied_quota')
    await expect(page.getByTestId('audit-row-audit-2')).toContainText('too-many-today')
  })
})
