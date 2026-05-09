// ADR 0067 — annotation toolbar journey.
//
// Coverage:
//   1. Image documents render the image annotation layer with a
//      toolbar (rect, ellipse, arrow, comment) + visibility toggle.
//   2. Visibility toggle hides the SVG overlay.
//   3. Video documents render the video pin layer with a single
//      "Pin" tool + visibility toggle.
//
// Real canvas drawing (mouse-drag to draw a rect, click-to-pin a
// timestamp) needs a real browser canvas and lands in the
// integration suite. This spec pins the structural surface.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'

test.describe('Journey 49 — annotation layers', () => {
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
    await page.route('**/api/v1/documents/*/versions/*/annotations', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ annotations: [] }) }),
    )
  })

  test('image viewer shows toolbar + overlay', async ({ page }) => {
    await page.route('**/api/v1/documents/img-1', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: 'img-1', title: 'photo.jpg',
          workspace_id: 'w-1', folder_id: 'f-1',
          mime_type: 'image/jpeg', current_version_id: 'v-1',
          lifecycle_state: 'draft',
        }),
      }),
    )
    await page.goto('/workspaces/w-1/documents/img-1')

    await expect(page.getByTestId('annotation-toolbar')).toBeVisible()
    await expect(page.getByTestId('tool-image-rect')).toBeVisible()
    await expect(page.getByTestId('tool-image-ellipse')).toBeVisible()
    await expect(page.getByTestId('tool-image-arrow')).toBeVisible()
    await expect(page.getByTestId('tool-image-note')).toBeVisible()
    await expect(page.getByTestId('annotation-visibility-toggle')).toBeVisible()
    // Overlay layer is rendered.
    await expect(page.getByTestId('image-annotation-overlay')).toBeAttached()
  })

  test('visibility toggle hides the image overlay', async ({ page }) => {
    await page.route('**/api/v1/documents/img-1', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: 'img-1', title: 'photo.jpg',
          workspace_id: 'w-1', folder_id: 'f-1',
          mime_type: 'image/jpeg', current_version_id: 'v-1',
          lifecycle_state: 'draft',
        }),
      }),
    )
    await page.goto('/workspaces/w-1/documents/img-1')

    await expect(page.getByTestId('image-annotation-overlay')).toBeAttached()
    await page.getByTestId('annotation-visibility-toggle').click()
    await expect(page.getByTestId('image-annotation-overlay')).not.toBeAttached()
  })

  test('video viewer shows pin tool + element', async ({ page }) => {
    await page.route('**/api/v1/documents/vid-1', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: 'vid-1', title: 'clip.mp4',
          workspace_id: 'w-1', folder_id: 'f-1',
          mime_type: 'video/mp4', current_version_id: 'v-1',
          lifecycle_state: 'draft',
        }),
      }),
    )
    await page.goto('/workspaces/w-1/documents/vid-1')

    await expect(page.getByTestId('tool-video-pin')).toBeVisible()
    await expect(page.getByTestId('video-element')).toBeVisible()
  })
})
