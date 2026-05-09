// ADR 0071 — third-party e-sign connector journey (mocked).
//
// Coverage:
//   1. /admin/integrations renders the two-tab UI.
//   2. Connect → POST /esign/oauth/start → page navigates to vendor.
//   3. Disconnect → POST /esign/disconnect → row flips to "Not connected".
//   4. Envelopes tab lists in-progress rows.
//   5. /signatures/send/<doc> picks DocuSign and submits → POST
//      /signatures/requests then POST /signatures/esign/send.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'

test.describe('Journey 53 — DocuSign / Adobe Sign connectors', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: USER, email: 'me@example.com', display_name: 'Admin',
          role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
        }),
      }),
    )
    await page.route('**/api/v1/tasks/mine**', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  })

  test('integrations page shows connect / disconnect + envelope tab', async ({ page }) => {
    await page.route('**/api/v1/signatures/esign/connections', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          connections: [{
            provider: 'docusign', account_id: 'acct-1', base_uri: 'https://demo',
            connected_at: new Date().toISOString(),
            expires_at: new Date(Date.now() + 3600_000).toISOString(),
          }],
        }),
      }))
    await page.route('**/api/v1/signatures/esign/envelopes', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          envelopes: [{
            request_id: '00000000-0000-0000-0000-00000000aaaa',
            envelope_id: 'E-1', provider: 'docusign', status: 'in_progress',
          }],
        }),
      }))

    await page.goto('/admin/integrations')
    await expect(page.getByTestId('connections-section')).toBeVisible()
    await expect(page.getByTestId('status-docusign')).toContainText('Connected')
    await expect(page.getByTestId('status-adobe_sign')).toContainText('Not connected')

    await page.getByTestId('tab-envelopes').click()
    await expect(page.getByTestId('envelopes-table')).toBeVisible()
    await expect(page.getByTestId('envelope-row-E-1')).toContainText('docusign')
  })

  test('connect button kicks off OAuth redirect', async ({ page }) => {
    await page.route('**/api/v1/signatures/esign/connections', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ connections: [] }) }))
    await page.route('**/api/v1/signatures/esign/envelopes', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ envelopes: [] }) }))
    let oauthBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/signatures/esign/oauth/start', async (route) => {
      oauthBody = JSON.parse(route.request().postData() ?? '{}')
      return route.fulfill({
        status: 200, contentType: 'application/json',
        // Use a relative path so the test doesn't navigate away.
        body: JSON.stringify({ redirect_url: '/admin/integrations?connected=docusign' }),
      })
    })
    await page.goto('/admin/integrations')
    await page.getByTestId('connect-docusign').click()
    await expect.poll(() => oauthBody).not.toBeNull()
    expect(oauthBody!.provider).toBe('docusign')
  })

  test('disconnect flips status to "Not connected"', async ({ page }) => {
    let connected = true
    await page.route('**/api/v1/signatures/esign/connections', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          connections: connected ? [{
            provider: 'docusign', account_id: 'a',
            connected_at: new Date().toISOString(),
            expires_at: new Date(Date.now() + 3600_000).toISOString(),
          }] : [],
        }),
      }))
    await page.route('**/api/v1/signatures/esign/envelopes', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ envelopes: [] }) }))
    await page.route('**/api/v1/signatures/esign/disconnect', (route) => {
      connected = false
      return route.fulfill({ status: 204, body: '' })
    })

    await page.goto('/admin/integrations')
    await expect(page.getByTestId('status-docusign')).toContainText('Connected')
    await page.getByTestId('disconnect-docusign').click()
    await expect(page.getByTestId('status-docusign')).toContainText('Not connected')
  })

  test('Send-via flow POSTs request then esign send', async ({ page }) => {
    const docId = '00000000-0000-0000-0000-0000000001bb'
    await page.route(`**/api/v1/documents/${docId}/content`, (r) =>
      r.fulfill({ status: 200, contentType: 'application/pdf', body: Buffer.from('%PDF-mock') }))
    let createdReq: Record<string, unknown> | null = null
    await page.route('**/api/v1/signatures/requests', (route) => {
      createdReq = JSON.parse(route.request().postData() ?? '{}')
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({
          id: 'req-1', document_id: docId, version_id: docId,
          status: 'pending', provider: 'docusign', signers: [],
          created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
        }),
      })
    })
    let sentBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/signatures/esign/send', (route) => {
      sentBody = JSON.parse(route.request().postData() ?? '{}')
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({ envelope_id: 'env-1', status: 'in_progress' }),
      })
    })
    // The send flow navigates to /admin/integrations on success; stub
    // its data calls so it renders without errors.
    await page.route('**/api/v1/signatures/esign/connections', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ connections: [] }) }))
    await page.route('**/api/v1/signatures/esign/envelopes', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ envelopes: [] }) }))

    await page.goto(`/signatures/send/${docId}`)
    await expect(page.getByTestId('esign-provider-docusign')).toBeVisible()
    await page.getByTestId('esign-provider-docusign').click()
    await page.getByTestId('recipient-email-0').fill('alice@example.com')
    await page.getByTestId('recipient-name-0').fill('Alice')
    await page.getByTestId('esign-send').click()

    await expect.poll(() => createdReq).not.toBeNull()
    expect(createdReq!.provider).toBe('docusign')
    await expect.poll(() => sentBody).not.toBeNull()
    expect(sentBody!.provider).toBe('docusign')
    expect(sentBody!.request_id).toBe('req-1')
  })
})
