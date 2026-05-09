// ADR 0070 — eIDAS QES journey (mocked TSP).
//
// Coverage:
//   1. /sign/:request/:signer renders the type selector.
//   2. Picking "Qualified" reveals the TSP picker.
//   3. Click "Continue" → POST /qes/start → page navigates to the
//      mock redirect URL.
//   4. /sign/done with status=completed renders the cert block,
//      pulled from /qes/certificates.
//   5. /sign/done with status=failed shows the failure card +
//      reason copy.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'
const REQUEST_ID = '00000000-0000-0000-0000-0000000000aa'
const SIGNER_ID  = '00000000-0000-0000-0000-0000000000bb'
const SESSION_ID = '00000000-0000-0000-0000-0000000000cc'

test.describe('Journey 52 — QES signing', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: USER, email: 'me@example.com', display_name: 'Me',
          role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
        }),
      }),
    )
    await page.route('**/api/v1/tasks/mine**', (r) =>
      r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  })

  test('type selector renders + qualified shows TSP picker', async ({ page }) => {
    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: REQUEST_ID, document_id: 'doc-1', version_id: 'v-1',
          status: 'in_progress', provider: 'internal',
          signers: [{ id: SIGNER_ID, email: 'me@example.com', name: 'Me', role: 'signer', status: 'pending' }],
          created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
        }),
      }))

    await page.goto(`/sign/${REQUEST_ID}/${SIGNER_ID}`)
    await expect(page.getByTestId('sig-type-selector')).toBeVisible()
    await expect(page.getByTestId('sig-type-simple')).toBeVisible()
    await expect(page.getByTestId('sig-type-advanced')).toBeVisible()
    await expect(page.getByTestId('sig-type-qualified')).toBeVisible()

    // Qualified hides QES block by default; clicking surfaces it.
    await expect(page.getByTestId('qes-block')).toBeHidden()
    await page.getByTestId('sig-type-qualified').click()
    await expect(page.getByTestId('qes-block')).toBeVisible()
    await expect(page.getByTestId('qes-provider-select')).toBeVisible()
  })

  test('start QES POSTs and redirects', async ({ page, context }) => {
    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: REQUEST_ID, document_id: 'doc-1', version_id: 'v-1',
          status: 'in_progress', provider: 'internal',
          signers: [{ id: SIGNER_ID, email: 'me@example.com', name: 'Me', role: 'signer', status: 'pending' }],
          created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
        }),
      }))
    await page.route('**/api/v1/documents/doc-1/content', (route) =>
      route.fulfill({ status: 200, contentType: 'application/pdf', body: Buffer.from('%PDF-mock') }))

    let startBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/signatures/qes/start', async (route) => {
      startBody = JSON.parse(route.request().postData() ?? '{}')
      return route.fulfill({
        status: 201, contentType: 'application/json',
        body: JSON.stringify({
          session_id: SESSION_ID,
          redirect_url: '/sign/done?session=' + SESSION_ID + '&status=completed',
          provider: 'mock',
          expires_at: new Date(Date.now() + 900_000).toISOString(),
        }),
      })
    })
    await page.route(`**/api/v1/signatures/qes/session/${SESSION_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: SESSION_ID, request_id: REQUEST_ID, status: 'completed',
          provider: 'mock',
        }),
      }))
    await page.route('**/api/v1/signatures/qes/certificates**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ certificates: [] }),
      }))

    await page.goto(`/sign/${REQUEST_ID}/${SIGNER_ID}`)
    await page.getByTestId('sig-type-qualified').click()
    await page.getByTestId('qes-provider-select').selectOption('mock')
    await page.getByTestId('qes-start').click()
    await expect.poll(() => startBody).not.toBeNull()
    expect(startBody!.provider).toBe('mock')
    expect(startBody!.request_id).toBe(REQUEST_ID)
    // The mock redirect lands on /sign/done.
    await page.waitForURL('**/sign/done**')
    await expect(page.getByTestId('qes-status-completed')).toBeVisible()
  })

  test('completed return shows certificate block', async ({ page }) => {
    await page.route(`**/api/v1/signatures/qes/session/${SESSION_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: SESSION_ID, request_id: REQUEST_ID, status: 'completed', provider: 'mock',
        }),
      }))
    await page.route('**/api/v1/signatures/qes/certificates**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          certificates: [{
            id: 'cert-1', signer_id: SIGNER_ID, provider: 'mock',
            subject_dn: 'CN=Test,O=VaultDMS', issuer_dn: 'CN=Mock Root',
            serial_hex: 'aabbcc',
            not_before: new Date().toISOString(),
            not_after: new Date(Date.now() + 365 * 86400_000).toISOString(),
            created_at: new Date().toISOString(),
          }],
        }),
      }))

    await page.goto(`/sign/done?session=${SESSION_ID}&status=completed`)
    await expect(page.getByTestId('qes-status-completed')).toBeVisible()
    await expect(page.getByTestId('qes-cert-block')).toBeVisible()
    await expect(page.getByTestId('qes-cert-cert-1')).toContainText('CN=Test')
  })

  test('failed return shows reason', async ({ page }) => {
    await page.route(`**/api/v1/signatures/qes/session/${SESSION_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: SESSION_ID, request_id: REQUEST_ID, status: 'failed',
          provider: 'mock', failure_reason: 'expired',
        }),
      }))
    await page.goto(`/sign/done?session=${SESSION_ID}&status=failed&reason=expired`)
    await expect(page.getByTestId('qes-status-failed')).toBeVisible()
    await expect(page.getByTestId('qes-status-failed')).toContainText('expired')
  })
})
