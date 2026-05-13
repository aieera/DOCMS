// ADR 0076 — /admin/webhooks journey. Covers create + test-send +
// view delivery log + re-drive DLQ + Verify panel with per-language
// signature samples. All backend calls mocked — the Playwright sweep
// runs against the FE in isolation.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'
const WH_ID     = 'wh_11111111111111111111111111111111'

function ownerSession(page: import('@playwright/test').Page) {
  return Promise.all([
    page.route('**/api/v1/auth/login', (r) =>
      r.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'owner@example.com', display_name: 'Owner',
            role: 'owner', status: 'active', mfa_enabled: false,
          },
          tenant_id: TENANT_ID,
        }),
      }),
    ),
    page.route('**/api/v1/auth/me', (r) =>
      r.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'owner@example.com', display_name: 'Owner',
          role: 'owner', tenant_id: TENANT_ID,
        }),
      }),
    ),
  ])
}

test.describe('Journey 56 — Admin customer webhooks', () => {
  test.beforeEach(async ({ page }) => {
    await ownerSession(page)
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('owner@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('create + test-send + view delivery + redeliver DLQ row', async ({ page }) => {
    // Start with one active webhook in the list.
    const webhooks = [
      {
        id: WH_ID, tenant_id: TENANT_ID,
        url: 'https://example.com/hooks/vaultdms',
        events: ['dms.document.created.v1'],
        active: true, created_by: USER_ID,
        created_at: '2026-05-13T00:00:00Z',
      },
    ]
    await page.route('**/api/v1/webhooks', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(webhooks) })
      }
      // POST create — return a new webhook with the one-shot secret.
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'wh_new', tenant_id: TENANT_ID,
          url: 'https://hook.example/x', events: ['dms.document.created.v1'],
          active: true, created_by: USER_ID, created_at: '2026-05-13T00:00:01Z',
          secret: 'whsec_deadbeefcafebabe',
        }),
      })
    })

    // Test-send returns 202 with a pending delivery.
    let testSendCalled = false
    await page.route(`**/api/v1/webhooks/${WH_ID}/test`, async (route) => {
      testSendCalled = true
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'd_test', subscription_id: WH_ID,
          event_type: 'dms.webhook.test.v1',
          status_code: 0, attempts: 0, dead_lettered: false,
          created_at: '2026-05-13T00:00:02Z',
        }),
      })
    })

    // Deliveries: one delivered + one DLQ.
    let redeliverCalled = false
    await page.route(`**/api/v1/webhooks/${WH_ID}/deliveries`, async (route) => {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          {
            id: 'd_ok', subscription_id: WH_ID, event_type: 'dms.document.created.v1',
            status_code: 200, attempts: 1, dead_lettered: false,
            delivered_at: '2026-05-13T00:01:00Z',
            created_at: '2026-05-13T00:01:00Z',
          },
          {
            id: 'd_dlq', subscription_id: WH_ID, event_type: 'dms.document.updated.v1',
            status_code: 500, attempts: 6, dead_lettered: true,
            created_at: '2026-05-13T00:00:30Z',
          },
        ]),
      })
    })
    await page.route(`**/api/v1/webhooks/${WH_ID}/deliveries/d_dlq/redeliver`, async (route) => {
      redeliverCalled = true
      return route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'd_dlq_clone', subscription_id: WH_ID,
          event_type: 'dms.document.updated.v1',
          status_code: 0, attempts: 0, dead_lettered: false,
          created_at: '2026-05-13T00:02:00Z',
        }),
      })
    })

    await page.goto('/admin/webhooks')

    // Header copy matches the actual policy (6-step backoff).
    await expect(page.getByRole('heading', { name: /webhooks/i })).toBeVisible()
    await expect(page.getByText(/5s \/ 30s \/ 2m \/ 15m \/ 1h \/ 6h/i)).toBeVisible()

    // Existing webhook shows up.
    await expect(page.getByText('https://example.com/hooks/vaultdms')).toBeVisible()

    // Create flow — bad URL should toast, then a valid one should pop the one-shot secret.
    await page.getByPlaceholder(/your-service\.example\.com/i).fill('not-a-url')
    await page.getByText('dms.document.created.v1', { exact: true }).first().click()
    await page.getByRole('button', { name: /create webhook/i }).click()
    await expect(page.getByText(/must start with http/i)).toBeVisible()
    await page.getByPlaceholder(/your-service\.example\.com/i).fill('https://hook.example/x')
    await page.getByRole('button', { name: /create webhook/i }).click()
    await expect(page.getByText(/whsec_deadbeefcafebabe/)).toBeVisible()

    // Test send.
    await page.getByTestId(`webhook-test-${WH_ID}`).click()
    await expect.poll(() => testSendCalled).toBeTruthy()
    await expect(page.getByText(/test delivery queued/i)).toBeVisible()

    // Verify panel — language switcher renders curl snippet with the
    // documented signing input.
    await page.getByTestId(`webhook-verify-${WH_ID}`).click()
    await page.getByTestId('verify-lang-curl').click()
    await expect(page.getByText('openssl dgst -sha256 -hmac')).toBeVisible()
    await page.getByTestId('verify-lang-python').click()
    await expect(page.getByText('hmac.compare_digest')).toBeVisible()

    // Expand the webhook row → see delivery log, DLQ row, redeliver.
    await page.getByText('https://example.com/hooks/vaultdms').click()
    await expect(page.getByText('DLQ')).toBeVisible()
    await page.getByRole('button', { name: /redeliver/i }).first().click()
    await expect.poll(() => redeliverCalled).toBeTruthy()
    await expect(page.getByText(/queued for redelivery/i)).toBeVisible()
  })
})
