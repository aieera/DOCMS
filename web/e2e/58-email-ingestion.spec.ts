// ADR 0087 — /admin/integrations/email journey. Source picker decides
// which fields are required; IMAP gets host + user + password while
// Microsoft / Gmail rely on the existing connector OAuth. Status pane
// surfaces totals + last error.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

function ownerSession(page: import('@playwright/test').Page) {
  return Promise.all([
    page.route('**/api/v1/auth/login', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
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
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'owner@example.com', display_name: 'Owner',
          role: 'owner', tenant_id: TENANT_ID,
        }),
      }),
    ),
  ])
}

test.describe('Journey 58 — Email ingestion admin', () => {
  test.beforeEach(async ({ page }) => {
    await ownerSession(page)
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('owner@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('create IMAP config, status pane shows error, run-now mutates ingested count', async ({ page }) => {
    let configs: any[] = []
    await page.route('**/api/v1/admin/email-configs', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(configs) })
      }
      const body = route.request().postDataJSON()
      const newConfig = {
        id: 'cfg-1', tenant_id: TENANT_ID,
        source: body.source, label: body.label,
        active: true, imap_host: body.imap_host, imap_port: body.imap_port,
        imap_use_tls: true, imap_username: body.imap_username,
        poll_interval_seconds: body.poll_interval_seconds ?? 300,
        messages_ingested: 0,
        last_error: 'auth failed: invalid credentials',
        created_at: '2026-05-13T00:00:00Z',
      }
      configs = [newConfig]
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(newConfig) })
    })
    let runCalled = false
    await page.route('**/api/v1/admin/email-configs/cfg-1/run', async (route) => {
      runCalled = true
      configs = configs.map((c) => ({ ...c, messages_ingested: 3, last_error: '', last_success_at: '2026-05-13T01:00:00Z' }))
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ingested: 3 }) })
    })
    await page.route('**/api/v1/admin/email-configs/cfg-1/stats', async (route) => {
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          config_id: 'cfg-1',
          messages_total: configs[0]?.messages_ingested ?? 0,
          messages_pending: 0,
          messages_failed: 0,
          last_error: configs[0]?.last_error || undefined,
          next_run_at: '2026-05-13T01:05:00Z',
        }),
      })
    })

    await page.goto('/admin/integrations/email')
    await expect(page.getByRole('heading', { name: /email ingestion/i })).toBeVisible()

    // Start a new config — validation rejects empty label.
    await page.getByTestId('email-config-new').click()
    await page.getByTestId('email-create').click()
    await expect(page.getByText(/label is required/i)).toBeVisible()

    await page.getByTestId('email-label').fill('imap-support')
    await page.getByTestId('email-imap-host').fill('imap.example.com')
    await page.getByTestId('email-imap-user').fill('user@example.com')
    await page.getByTestId('email-imap-pwd').fill('hunter2')
    await page.getByTestId('email-create').click()
    await expect(page.getByText(/config created/i)).toBeVisible()

    // Status pane shows the simulated last error.
    await page.getByText('imap-support').click()
    await expect(page.getByTestId('email-error-cfg-1')).toBeVisible()
    await expect(page.getByText(/auth failed/i)).toBeVisible()

    // Run-now path.
    await page.getByTestId('email-run-cfg-1').click()
    await expect.poll(() => runCalled).toBeTruthy()
    await expect(page.getByText(/3 new messages/i)).toBeVisible()
  })
})
