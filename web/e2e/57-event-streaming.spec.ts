// ADR 0077 — /admin/integrations/events journey. Covers token issue
// (one-shot bearer + creds reveal), language switcher for connection
// samples, revoke flow, and that the event-type reference panel
// renders the curated prefix list. SSE live-tail is mocked rather
// than exercised end-to-end (the FE just wires window.EventSource).

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

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

test.describe('Journey 57 — Event streaming admin', () => {
  test.beforeEach(async ({ page }) => {
    await ownerSession(page)
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('owner@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('issue token, reveal once, switch language samples, revoke', async ({ page }) => {
    let tokens: any[] = []
    await page.route('**/api/v1/admin/event-stream/tokens', async (route) => {
      const method = route.request().method()
      if (method === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(tokens) })
      }
      // POST issue
      const body = route.request().postDataJSON() as { label: string }
      const newTok = {
        id: 'tev_id_1',
        label: body.label,
        bearer_token: 'tev_aabbccddeeff112233445566778899aa',
        nats_creds_file:
          '-----BEGIN NATS USER JWT-----\nfake-jwt\n------END NATS USER JWT------\n\n' +
          '-----BEGIN USER NKEY SEED-----\nSUAxxx\n------END USER NKEY SEED------',
        nats_account_id: 'ACCxxx',
        created_at: '2026-05-13T00:00:00Z',
        expires_at: '2026-06-12T00:00:00Z',
        revoked: false,
      }
      tokens = [{ ...newTok, bearer_token: undefined, nats_creds_file: undefined }]
      return route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify(newTok) })
    })
    let revokeCalled = false
    await page.route('**/api/v1/admin/event-stream/tokens/tev_id_1', async (route) => {
      revokeCalled = true
      tokens = tokens.map((t) => ({ ...t, revoked: true }))
      return route.fulfill({ status: 204, body: '' })
    })

    await page.goto('/admin/integrations/events')
    await expect(page.getByRole('heading', { name: /event streaming/i })).toBeVisible()

    // Validation: empty label is rejected.
    await page.getByTestId('token-issue').click()
    await expect(page.getByText(/label is required/i)).toBeVisible()

    // Issue a token. The one-shot banner reveals bearer + creds.
    await page.getByTestId('token-label').fill('prod-receiver')
    await page.getByTestId('token-issue').click()
    await expect(page.getByTestId('issued-token-banner')).toBeVisible()
    await expect(page.getByText('tev_aabbccddeeff')).toBeVisible()
    await expect(page.getByText('-----BEGIN NATS USER JWT-----')).toBeVisible()
    await expect(page.getByTestId('creds-download')).toBeVisible()

    // Token list now shows the issued token but without the plaintext halves.
    await expect(page.getByText('prod-receiver').first()).toBeVisible()

    // Language switcher — each tab shows its language-specific snippet.
    await page.getByTestId('sample-lang-python').click()
    await expect(page.getByText('import asyncio, nats')).toBeVisible()
    await page.getByTestId('sample-lang-js').click()
    await expect(page.getByText('@nats-io/nats-core')).toBeVisible()
    await page.getByTestId('sample-lang-curl').click()
    await expect(page.getByText('VAULTDMS_EVENT_TOKEN')).toBeVisible()
    await page.getByTestId('sample-lang-go').click()
    await expect(page.getByText('nats.UserCredentials')).toBeVisible()

    // Event-type reference renders the curated prefix list.
    await expect(page.getByText('dms.document.*')).toBeVisible()
    await expect(page.getByText('dms.audit.*')).toBeVisible()

    // Revoke flow.
    await page.getByRole('button', { name: /revoke/i }).first().click()
    await page.getByRole('button', { name: 'Revoke', exact: true }).click()
    await expect.poll(() => revokeCalled).toBeTruthy()
    await expect(page.getByText(/token revoked/i)).toBeVisible()
  })
})
