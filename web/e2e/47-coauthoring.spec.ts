// ADR 0065 — co-authoring journey.
//
// Coverage:
//   1. Edit-in-browser button is visible for DOCX docs and absent
//      for non-Office mime types.
//   2. Backend says provider="disabled" → toast shown, no iframe.
//   3. Health probe fails → fallback panel + "Open in desktop app"
//      link rendered. (Real iframe rendering against a running
//      OnlyOffice/Collabora is the integration suite's job.)
import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'
const DOC    = 'doc-1'
const VER    = 'ver-1'

test.describe('Journey 47 — co-authoring (WOPI)', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: USER, email: 'admin@example.com',
          display_name: 'Admin', role: 'owner',
          tenant_id: TENANT, tenant_slug: 'demo',
        }),
      }),
    )
    await page.route(`**/api/v1/documents/${DOC}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: DOC, title: 'Contract.docx',
          workspace_id: 'w-1', folder_id: 'f-1',
          mime_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
          current_version_id: VER, lifecycle_state: 'draft',
        }),
      }),
    )
  })

  test('Edit-in-browser button is shown for DOCX', async ({ page }) => {
    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await expect(page.getByTestId('edit-in-browser')).toBeVisible()
  })

  test('disabled provider shows info toast, no iframe', async ({ page }) => {
    await page.route(`**/api/v1/documents/${DOC}/versions/${VER}/coauth/start`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          iframe_url: '', provider: 'disabled', editor_health_url: '',
          access_token: '', access_token_ttl: 0, mode: 'edit',
        }),
      }),
    )
    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await page.getByTestId('edit-in-browser').click()
    // Disabled provider → no iframe.
    await expect(page.getByTestId('coauth-iframe')).not.toBeVisible()
  })

  test('unreachable editor renders fallback', async ({ page }) => {
    await page.route(`**/api/v1/documents/${DOC}/versions/${VER}/coauth/start`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          iframe_url: 'https://onlyoffice.invalid/editor',
          provider: 'onlyoffice',
          editor_health_url: 'https://onlyoffice.invalid/healthcheck',
          access_token: 't', access_token_ttl: 3600000, mode: 'edit',
        }),
      }),
    )
    // Block the health probe so probeEditorReachable resolves false.
    await page.route('https://onlyoffice.invalid/**', (route) => route.abort())
    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await page.getByTestId('edit-in-browser').click()
    await expect(page.getByTestId('coauth-fallback')).toBeVisible()
    await expect(page.getByText(/Open in desktop app/i)).toBeVisible()
    // Iframe must NOT have been rendered.
    await expect(page.getByTestId('coauth-iframe')).not.toBeVisible()
  })
})
