// Journey 05 — document upload.
// Log in → navigate to a workspace → drag-drop or click to upload →
// upload-initiate + presigned PUT + complete all mocked.

import { test, expect } from '@playwright/test'
import path from 'path'
import { fileURLToPath } from 'url'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER   = '00000000-0000-0000-0000-000000000001'
const WORKSPACE = '00000000-0000-0000-0000-000000000200'
const FOLDER    = '00000000-0000-0000-0000-000000000300'

test.describe('Journey 05 — upload', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ id: USER, email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: TENANT }),
      }),
    )
    await page.route('**/api/v1/workspaces/*/folders/*/documents**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ items: [], next_page_token: null }),
      }),
    )
    // Upload flow: initiate → client PUTs to presigned URL → complete.
    let initiateCalls = 0
    await page.route('**/api/v1/storage/uploads/initiate', (route) => {
      initiateCalls++
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          upload_id: 'upl-' + initiateCalls,
          presigned_put_url: 'http://mocked-s3/put/' + initiateCalls,
        }),
      })
    })
    // Presigned PUT: short-circuit the network call.
    await page.route('http://mocked-s3/**', (route) =>
      route.fulfill({ status: 200, body: '' }),
    )
    await page.route('**/api/v1/storage/uploads/*/complete', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ document_id: '00000000-0000-0000-0000-0000000000aa', status: 'completed' }),
      }),
    )
  })

  test('click-to-browse uploads a file', async ({ page, context }) => {
    await context.addCookies([{
      name: 'dms_session', value: 'sess-upload', domain: '127.0.0.1', path: '/',
    }])
    await page.goto(`/workspaces/${WORKSPACE}/folders/${FOLDER}`)
    // Dropzone renders a hidden <input type=file>. Playwright can set
    // files directly on that input regardless of the dropzone wrapper.
    const __dirname = path.dirname(fileURLToPath(import.meta.url))
    const fixturePath = path.join(__dirname, 'fixtures', 'sample.txt')
    const fileChooserPromise = page.waitForEvent('filechooser')
    await page.getByText(/drag & drop files/i).click()
    const chooser = await fileChooserPromise
    await chooser.setFiles({ name: 'sample.txt', mimeType: 'text/plain', buffer: Buffer.from('hello world') })

    // Upload initiate fired.
    await page.waitForRequest((req) =>
      req.url().includes('/storage/uploads/initiate') && req.method() === 'POST',
    )
  })
})
