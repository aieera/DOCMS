// ADR 0073 — mobile + in-person signing journeys.
//
// Coverage:
//   1. /sign/:request/:signer rendered in a mobile viewport (iPhone)
//      surfaces the SignaturePad for the simple/advanced types.
//      Drawing on the canvas + tapping "Apply signature" POSTs to
//      the per-signer record endpoint with svg_path + device_kind.
//   2. /sign/in-person/:request walks signer → witness sequentially
//      on a tablet viewport. Hand-off prompt appears between steps;
//      the page flips to its own "All signers complete" state after
//      the last signer.
//
// The signature endpoints are stubbed so the test stays
// self-contained.

import { test, expect, devices } from '@playwright/test'

const TENANT = 't-1'
const USER = 'u-1'
const REQUEST_ID = '00000000-0000-0000-0000-0000000000aa'
const SIGNER_ID = '00000000-0000-0000-0000-0000000000bb'
const WITNESS_ID = '00000000-0000-0000-0000-0000000000cc'
// S-13: a remote/mobile signing link is /sign/{req}/{signer}?token=<hex>;
// without a well-formed token the page shows "incomplete link" instead of
// the pad, and the record call carries the token as ?token=.
const SIGN_TOKEN = '0123456789abcdef0123456789abcdef'

// A device descriptor also carries `defaultBrowserType`, and Playwright
// rejects `test.use({ defaultBrowserType })` inside a describe (it would
// need a new worker). Take only the context-level emulation fields: the
// test stays on the project's browser (chromium in CI) with the device's
// viewport, user agent, DPR, touch and isMobile, which is what drives
// pointer:coarse and the phone/tablet device_kind split.
const emulate = (name: string) => {
  const { viewport, userAgent, deviceScaleFactor, isMobile, hasTouch } = devices[name]
  return { viewport, userAgent, deviceScaleFactor, isMobile, hasTouch }
}

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
  // (No document-content stub: S-14 removed the client-side download +
  // SHA-256 of the document; the server hashes the version it holds.)
}

test.describe('Journey 54 — mobile signing', () => {
  test.use(emulate('iPhone 13'))

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
    let recordedToken: string | null = null
    // Predicate, not a glob: the record call carries ?token=.
    const recordPath = `/api/v1/signatures/requests/${REQUEST_ID}/sign/${SIGNER_ID}`
    await page.route((url) => url.pathname === recordPath, async (route) => {
      recordedBody = JSON.parse(route.request().postData() || '{}')
      recordedToken = new URL(route.request().url()).searchParams.get('token')
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: 'signed' }) })
    })

    await page.goto(`/sign/${REQUEST_ID}/${SIGNER_ID}?token=${SIGN_TOKEN}`)

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
    // S-13: the link's token is forwarded on the record call.
    expect(recordedToken).toBe(SIGN_TOKEN)
    // S-14: the client no longer computes or sends a document hash
    // (this used to assert doc_hash_sha256 was a 64-hex digest).
    expect(recordedBody).not.toHaveProperty('doc_hash_sha256')
  })
})

test.describe('Journey 54b — in-person signing', () => {
  // Tablet viewport: pointer:coarse + width > 768 → device_kind=tablet.
  test.use(emulate('iPad (gen 7) landscape'))

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

    // Last signer → the page shows its own completion state. It used to
    // push to /sign/done, the QES return page, which dead-ended without a
    // ?session= param; the ceremony now stays on this route.
    await expect(page.getByTestId('in-person-complete')).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`/sign/in-person/${REQUEST_ID}$`))
    expect((lastInPersonBody as Record<string, unknown>).signer_id).toBe(WITNESS_ID)
  })
})
