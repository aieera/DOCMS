// Journey 16 — MIME-mismatch handling in the uploader.
//
// Blueprint §22 anti-pattern #6: the backend detects that declared
// Content-Type disagrees with magic-byte MIME and rejects with
// 409 + type: MIME_MISMATCH. The uploader catches that and:
//   - flips the per-file status to 'mime_mismatch' (amber inline alert)
//   - renders "File type doesn't match extension" copy
//   - exposes a link to the help article
//
// This spec stubs initiate (success) → presign PUT (success) → complete
// (409 MIME_MISMATCH) and asserts the inline alert + help link render.
// No real file bytes leave the browser — Playwright's setInputFiles
// feeds a small PDF-shaped blob through the dropzone's hidden input.

import { test, expect } from '@playwright/test'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'
const WORKSPACE = '00000000-0000-0000-0000-000000000200'
const FOLDER = '00000000-0000-0000-0000-000000000300'

test.describe('Journey 16 — upload MIME mismatch', () => {
  test.beforeEach(async ({ page }) => {
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
    await page.route('**/api/v1/tenant/plan', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ plan: 'standard' }) }),
    )

    // Initiate → success; return a dummy presigned URL we intercept below.
    await page.route('**/api/v1/storage/uploads/initiate', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          upload_id: '11111111-1111-1111-1111-111111111111',
          presigned_put_url: 'http://localhost:9999/fake-presigned',
          storage_bucket: 'dms-us-east-1-hot',
          storage_key: 'k',
          expires_at: new Date(Date.now() + 3600_000).toISOString(),
          deduplicated: false,
        }),
      }),
    )
    await page.route('**/fake-presigned', (route) => route.fulfill({ status: 200, body: '' }))

    // Complete → MIME mismatch (the key assertion).
    await page.route('**/api/v1/storage/uploads/*/complete', (route) =>
      route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({
          type: 'MIME_MISMATCH',
          message: "file type doesn't match extension",
          declared_mime: 'image/png',
          detected_mime: 'application/pdf',
          correlation_id: '22222222-2222-2222-2222-222222222222',
        }),
      }),
    )
  })

  test('renamed PDF uploaded as PNG shows inline mismatch alert + help link', async ({ page }) => {
    await page.goto(`/workspaces/${WORKSPACE}/folders/${FOLDER}`)

    // Dropzone's hidden input accepts setInputFiles even when the visible
    // affordance is a drag-and-drop zone.
    const hiddenInput = page.locator('[data-testid="document-upload-dropzone"] input[type="file"]')
    await hiddenInput.setInputFiles({
      // Claim image/png but send the PDF magic-byte header. Backend
      // would reject server-side; here we only need the backend stub
      // above to return MIME_MISMATCH.
      name: 'report.png',
      mimeType: 'image/png',
      buffer: Buffer.from('%PDF-1.4\n%\xe2\xe3\xcf\xd3\n'),
    })

    const row = page.locator('[data-testid^="upload-row-"]').first()
    await expect(row).toBeVisible()

    // Inline alert with help link.
    const alert = row.locator('[data-testid^="upload-mime-mismatch-"]')
    await expect(alert).toBeVisible()
    await expect(alert).toContainText("File type doesn't match extension")
    await expect(alert).toContainText('application/pdf')
    const help = alert.locator('a', { hasText: 'Learn more' })
    await expect(help).toHaveAttribute('href', /mime-mismatch/)
  })

  test('tier hint surfaces in the upload tooltip', async ({ page }) => {
    await page.goto(`/workspaces/${WORKSPACE}/folders/${FOLDER}`)
    const dropzone = page.getByTestId('document-upload-dropzone')
    await expect(dropzone).toHaveAttribute('title', /Max upload size: 5 GB \(Standard tier\)/)
    await expect(page.getByTestId('upload-tier-hint')).toContainText('5 GB')
    await expect(page.getByTestId('upload-tier-hint')).toContainText('Standard')
  })
})
