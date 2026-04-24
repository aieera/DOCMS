// Visual regression — one snapshot per key page.
//
// Uses Playwright's built-in toHaveScreenshot(). Snapshots live under
// e2e/__screenshots__/ (auto-created on first run) and are committed
// to the repo. PR reviewers see visual diffs when a page's markup or
// tokens change.
//
// Scope: three high-traffic pages. Expanding to every page is a
// backlog item — per-page snapshots cost ~100ms and grow the repo.
//
// First-run bootstrap: `npx playwright test --update-snapshots` to
// accept the initial snapshots, then commit them.

import { test, expect } from '@playwright/test'

test.describe.configure({ mode: 'serial' })

test.beforeEach(async ({ page, context }) => {
  // Minimal auth mocks so the authenticated layout renders. Visual
  // regression doesn't care about data freshness — the goal is a
  // stable markup snapshot.
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ id: 'u-1', email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: 't-1' }),
    }),
  )
  await page.route('**/api/v1/workspaces**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: '{"items":[]}' }),
  )
  await page.route('**/api/v1/workflows/tasks**', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
  )
  await context.addCookies([{ name: 'dms_session', value: 'sess-vis', domain: '127.0.0.1', path: '/' }])
})

test('login page snapshot', async ({ page }) => {
  await page.goto('/login')
  // Wait for the primary heading to paint to avoid pre-hydration snapshots.
  await expect(page.getByRole('heading', { name: /vaultdms/i })).toBeVisible()
  await expect(page).toHaveScreenshot('login.png', { fullPage: true, maxDiffPixelRatio: 0.01 })
})

test('authenticated dashboard snapshot', async ({ page }) => {
  await page.goto('/')
  await page.waitForLoadState('networkidle')
  await expect(page).toHaveScreenshot('dashboard.png', { fullPage: true, maxDiffPixelRatio: 0.01 })
})

test('tasks page snapshot (empty state)', async ({ page }) => {
  await page.goto('/tasks')
  await expect(page.getByRole('heading', { name: /my tasks/i })).toBeVisible()
  await expect(page).toHaveScreenshot('tasks.png', { fullPage: true, maxDiffPixelRatio: 0.01 })
})
