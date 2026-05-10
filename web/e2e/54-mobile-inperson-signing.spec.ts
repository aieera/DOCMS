// ADR 0073 — mobile + in-person signing journeys.
//
// Coverage:
//   1. /sign/:request/:signer rendered in a mobile viewport (iPhone)
//      surfaces the SignaturePad for the simple/advanced types.
//      Drawing on the canvas + tapping "Apply signature" POSTs to
//      the per-signer record endpoint with svg_path + device_kind.
//   2. /sign/in-person/:request walks signer → witness sequentially
//      on a tablet viewport. Hand-off prompt appears between steps;
//      navigates to /sign/done after the last signer.
//
// The TSP and document-bytes endpoints are stubbed so the test
// stays self-contained.

import { test, expect, devices } from '@playwright/test'

const TENANT = 't-1'
const USER = 'u-1'
const REQUEST_ID = '00000000-0000-0000-0000-0000000000aa'
const SIGNER_ID = '00000000-0000-0000-0000-0000000000bb'
const WITNESS_ID = '00000000-0000-0000-0000-0000000000cc'

const authStubs = async (page: import('@playwright/test').Page) => {
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({
        id: USER, email: 'me@example.com', display_name: 'Me',
        role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
      }),
    }))
  await page.route('**/api/v1/tasks/mine**', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  // Document content stub — the page tries to fetch + hash these
  // bytes for tamper-detection. Returns a tiny fixed payload so the
  // SHA-256 is deterministic.
  await page.route('**/api/v1/documents/*/content', (route) =>
    route.fulfill({ status: 200, contentType: 'application/pdf', body: 'fake-pdf-bytes' }))
}

test.describe('Journey 54 — mobile signing', () => {
  test.use({ ...devices['iPhone 13'] })

  test.beforeEach(async ({ page }) => {
    await authStubs(page)
  })

  test('signature pad renders + records on mobile viewport', async ({ page }) => {
    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: REQUEST_ID, document_id: 'doc-1', version_id: 'v-1',
          status: 'in_progress', provider: 'internal',
          signing_mode: 'mobile',
          signers: [{ id: SIGNER_ID, email: 'me@example.com', name: 'Me', role: 'signer', status: 'pending' }],
          created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
        }),
      }))

    let recordedBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}/sign/${SIGNER_ID}`, async (route) => {
      recordedBody = JSON.parse(route.request().postData() || '{}')
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'signed' }) })
    })

    await page.goto(`/sign/${REQUEST_ID}/${SIGNER_ID}`)

    // Default type is 'advanced' — the SignaturePad should be shown,
    // not the QES block.
    await expect(page.getByTestId('signature-pad-canvas')).toBeVisible()
    await expect(page.getByTestId('qes-block')).toBeHidden()

    // Submit is disabled while the canvas is empty.
    await expect(page.getByTestId('signature-pad-submit')).toBeDisabled()

    // Draw a stroke on the canvas. Pointer events at canvas
    // coordinates — the SignaturePad uses Pointer Events so this
    // works even though the touch viewport is iPhone 13.
    const canvas = page.getByTestId('signature-pad-canvas')
    const box = await canvas.boundingBox()
    if (!box) throw new Error('canvas has no box')
    await canvas.dispatchEvent('pointerdown', { clientX: box.x + 20, clientY: box.y + 20, pointerId: 1, pointerType: 'touch' })
    await canvas.dispatchEvent('pointermove', { clientX: box.x + 80, clientY: box.y + 60, pointerId: 1, pointerType: 'touch' })
    await canvas.dispatchEvent('pointermove', { clientX: box.x + 140, clientY: box.y + 100, pointerId: 1, pointerType: 'touch' })
    await canvas.dispatchEvent('pointerup', { clientX: box.x + 140, clientY: box.y + 100, pointerId: 1, pointerType: 'touch' })

    // Submit should now be enabled, click it.
    await expect(page.getByTestId('signature-pad-submit')).toBeEnabled()
    await page.getByTestId('signature-pad-submit').click()

    // The page navigates to /sign/done on success — check the URL
    // landed there + the body the backend received included the
    // SVG path and device_kind.
    await expect(page).toHaveURL(/\/sign\/done/)
    expect(recordedBody).not.toBeNull()
    expect(typeof (recordedBody as Record<string, unknown>).svg_path).toBe('string')
    expect((recordedBody as Record<string, unknown>).svg_path).toMatch(/^M/)
    // device_kind should be 'phone' on the iPhone 13 viewport
    // (pointer:coarse + width <= 768).
    expect((recordedBody as Record<string, unknown>).device_kind).toBe('phone')
    // doc_hash_sha256 captured for tamper-detection.
    expect((recordedBody as Record<string, unknown>).doc_hash_sha256).toMatch(/^[0-9a-f]{64}$/)
  })
})

test.describe('Journey 54b — in-person signing', () => {
  // Tablet viewport: pointer:coarse + width > 768 → device_kind=tablet.
  test.use({ ...devices['iPad (gen 7) landscape'] })

  test.beforeEach(async ({ page }) => {
    await authStubs(page)
  })

  test('signer + witness flow on a single tablet device', async ({ page }) => {
    let signerSigned = false
    let witnessSigned = false
    let lastInPersonBody: Record<string, unknown> | null = null

    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: REQUEST_ID, document_id: 'doc-1', version_id: 'v-1',
          status: 'in_progress', provider: 'internal',
          signing_mode: 'in_person',
          signers: [
            { id: SIGNER_ID, email: 'signer@example.com', name: 'The Signer', role: 'signer', order: 1, status: signerSigned ? 'signed' : 'pending' },
            { id: WITNESS_ID, email: 'witness@example.com', name: 'The Witness', role: 'witness', order: 2, status: witnessSigned ? 'signed' : 'pending' },
          ],
          created_at: new Date().toISOString(), expires_at: new Date(Date.now() + 86400_000).toISOString(),
        }),
      }))

    await page.route(`**/api/v1/signatures/requests/${REQUEST_ID}/in-person/sign`, async (route) => {
      const body = JSON.parse(route.request().postData() || '{}')
      lastInPersonBody = body
      if (body.signer_id === SIGNER_ID && !signerSigned) {
        signerSigned = true
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'signed' }) })
        return
      }
      if (body.signer_id === WITNESS_ID && signerSigned && !witnessSigned) {
        witnessSigned = true
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'signed' }) })
        return
      }
      await route.fulfill({
        status: 409, contentType: 'application/json',
        body: JSON.stringify({ error: 'out of order', expected_signer_id: signerSigned ? WITNESS_ID : SIGNER_ID }),
      })
    })

    await page.goto(`/sign/in-person/${REQUEST_ID}`)

    // Step 1 — the signer block is visible (no hand-off prompt yet).
    await expect(page.getByTestId('in-person-pad-block')).toBeVisible()
    await expect(page.getByTestId('in-person-handoff')).toBeHidden()

    // Draw + submit signer 1.
    const draw = async () => {
      const canvas = page.getByTestId('signature-pad-canvas')
      const box = await canvas.boundingBox()
      if (!box) throw new Error('canvas has no box')
      await canvas.dispatchEvent('pointerdown', { clientX: box.x + 20, clientY: box.y + 20, pointerId: 1, pointerType: 'touch' })
      await canvas.dispatchEvent('pointermove', { clientX: box.x + 100, clientY: box.y + 80, pointerId: 1, pointerType: 'touch' })
      await canvas.dispatchEvent('pointerup', { clientX: box.x + 100, clientY: box.y + 80, pointerId: 1, pointerType: 'touch' })
    }
    await draw()
    await page.getByTestId('signature-pad-submit').click()

    // After step 1, we should see the hand-off prompt for the
    // witness, not the pad.
    await expect(page.getByTestId('in-person-handoff')).toBeVisible()
    await expect(page.getByTestId('in-person-handoff-continue')).toBeVisible()
    await expect(page.getByTestId('in-person-handoff')).toContainText('The Witness')
    expect((lastInPersonBody as Record<string, unknown>).signer_id).toBe(SIGNER_ID)
    expect((lastInPersonBody as Record<string, unknown>).device_kind).toBe('tablet')

    // Operator hands the device over.
    await page.getByTestId('in-person-handoff-continue').click()
    await expect(page.getByTestId('in-person-pad-block')).toBeVisible()

    // Witness signs.
    await draw()
    await page.getByTestId('signature-pad-submit').click()

    // Last signer → navigates to /sign/done.
    await expect(page).toHaveURL(/\/sign\/done/)
    expect((lastInPersonBody as Record<string, unknown>).signer_id).toBe(WITNESS_ID)
  })
})
