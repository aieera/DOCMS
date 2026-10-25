// ADR 0072 — PAdES validity badge journey.
//
// Coverage:
//   1. Badge renders the correct tone for valid / indeterminate /
//      tampered / unsigned reports.
//   2. Re-validate fetches the document bytes, POSTs them to
//      /signatures/verify-bytes and updates the badge in place.
//   3. LTV-age pill shows the human label.
//
// Drives the real SignatureValidityBadge where the app renders it: the
// document page's Collaboration → Signatures panel, which shows the badge
// once the document has at least one signature request. (This spec used
// to inject a hand-built badge <div> into document.body after fetching
// the mocks itself — it never exercised the component, and the stub sat
// under the app's sidebar, which intercepted the Re-validate click.)

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const WORKSPACE = 'w-1'
const DOC = '00000000-0000-0000-0000-000000000abc'

const baseAuth = async (page: import('@playwright/test').Page) => {
  await page.route('**/api/v1/auth/me', (r) =>
    r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ id: 'u-1', email: 'me@x', display_name: 'Me', role: 'owner', tenant_id: TENANT, tenant_slug: 'demo' }),
    }))
  await page.route('**/api/v1/tasks/mine**', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
}

test.describe('Journey 54 — PAdES validity badge', () => {
  test.beforeEach(async ({ page }) => {
    await baseAuth(page)
  })

  test('renders Valid tone for a clean LTV report', async ({ page }) => {
    await page.route('**/api/v1/signatures/verify-bytes', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          signature_count: 1,
          tamper_evident: true,
          ltv_enabled: true,
          ltv_age: 7 * 24 * 3600 * 1e9, // 7d in ns
          parsed_at: new Date().toISOString(),
          signatures: [{
            signer_name: 'Alice', cert_status: 'valid', level: 'PAdES-B-LT',
            chain_valid: true, timestamp_valid: true, tamper_evident: true,
          }],
        }),
      }))
    await page.route(`**/api/v1/documents/${DOC}/content`, (r) =>
      r.fulfill({ status: 200, contentType: 'application/pdf', body: Buffer.from('%PDF-test') }))

    await mountBadge(page)
    await page.getByTestId('signature-revalidate').click()
    await expect(page.getByTestId('signature-validity-badge')).toHaveAttribute('data-tone', 'valid')
    await expect(page.getByTestId('signature-validity-badge')).toContainText('1 valid signature')
    await expect(page.getByTestId('ltv-age')).toContainText('LTV 7d')
  })

  test('renders Indeterminate tone when cert expired since signing', async ({ page }) => {
    await page.route('**/api/v1/signatures/verify-bytes', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          signature_count: 1, tamper_evident: true, ltv_enabled: false,
          parsed_at: new Date().toISOString(),
          signatures: [{ signer_name: 'A', cert_status: 'indeterminate', level: 'PAdES-B-T',
                         chain_valid: true, timestamp_valid: true, tamper_evident: true }],
        }),
      }))
    await page.route(`**/api/v1/documents/${DOC}/content`, (r) =>
      r.fulfill({ status: 200, contentType: 'application/pdf', body: Buffer.from('%PDF') }))

    await mountBadge(page)
    await page.getByTestId('signature-revalidate').click()
    await expect(page.getByTestId('signature-validity-badge')).toHaveAttribute('data-tone', 'indeterminate')
    await expect(page.getByTestId('signature-validity-badge')).toContainText('expired since signing')
  })

  test('renders Invalid tone on tamper', async ({ page }) => {
    await page.route('**/api/v1/signatures/verify-bytes', (r) =>
      r.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          signature_count: 1, tamper_evident: false, ltv_enabled: false,
          parsed_at: new Date().toISOString(),
          signatures: [{ signer_name: 'A', cert_status: 'valid', level: 'PAdES-B-B',
                         chain_valid: true, timestamp_valid: false, tamper_evident: false }],
        }),
      }))
    await page.route(`**/api/v1/documents/${DOC}/content`, (r) =>
      r.fulfill({ status: 200, contentType: 'application/pdf', body: Buffer.from('%PDF') }))

    await mountBadge(page)
    await page.getByTestId('signature-revalidate').click()
    await expect(page.getByTestId('signature-validity-badge')).toHaveAttribute('data-tone', 'invalid')
    await expect(page.getByTestId('signature-validity-badge')).toContainText('Tampered')
  })

  test('renders Unsigned with no report', async ({ page }) => {
    await mountBadge(page)
    await expect(page.getByTestId('signature-validity-badge')).toHaveAttribute('data-tone', 'unsigned')
    await expect(page.getByTestId('signature-validity-badge')).toContainText('Unsigned')
  })
})

// mountBadge opens the document's Signatures panel with one signature
// request on file, so the panel renders the badge (tier1 = null →
// "Unsigned" until Re-validate runs).
async function mountBadge(page: import('@playwright/test').Page) {
  await page.route(`**/api/v1/documents/${DOC}`, (r) =>
    r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        id: DOC, title: 'Signed contract.pdf', workspace_id: WORKSPACE, folder_id: 'f-1',
        mime_type: 'application/pdf', current_version_id: 'v-1', lifecycle_state: 'active',
      }),
    }))
  await page.route(`**/api/v1/signatures/document/${DOC}`, (r) =>
    r.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([{
        id: 'req-1', document_id: DOC, version_id: 'v-1', status: 'completed',
        provider: 'internal', signers: [],
        created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
      }]),
    }))
  await page.goto(`/workspaces/${WORKSPACE}/documents/${DOC}`)
  await page.getByTestId('tabgroup-collaboration').click()
  await page.getByTestId('tab-signatures').click()
  await expect(page.getByTestId('signature-validity-badge')).toBeVisible()
}
