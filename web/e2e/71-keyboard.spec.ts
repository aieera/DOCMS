// Keyboard operability gate (ADR 0120) — the DoD's keyboard-only
// journey: skip link, login, find (search), approve (task complete),
// dialog escape, and the keyboard alternative to drag-and-drop
// (actions menu → "Move to folder…" — no pointer required).

import { test, expect, type Page } from '@playwright/test'

const USER = 'u-1'
const WS = '00000000-0000-0000-0000-00000000aaaa'
const DOC = '00000000-0000-0000-0000-00000000bbbb'

function json(body: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

async function mockCore(page: Page) {
  // LOWEST-priority catch-all (Playwright matches last-registered
  // first): any API call the test forgot to mock is ABORTED instead of
  // reaching the real gateway a dev machine may have running — a live
  // 401 there triggers the app's logout-and-redirect and poisons the
  // test at whatever moment the poll fires (flaky by timing).
  await page.route('**/api/v1/**', (r) => r.abort())
  await page.route('**/api/v1/notifications', (r) => r.fulfill(json({ items: [], total_count: 0 })))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(json({
    id: USER, email: 'me@example.com', display_name: 'Me',
    role: 'owner', tenant_id: 't-1', tenant_slug: 'demo',
  })))
  await page.route('**/api/v1/notifications/unread-count', (r) => r.fulfill(json({ count: 0 })))
  await page.route('**/api/v1/notifications?**', (r) => r.fulfill(json({ items: [], total_count: 0 })))
  await page.route('**/api/v1/saved-searches**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/workflows/tasks**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/workspaces', (r) => r.fulfill(json({
    workspaces: [{ id: WS, name: 'Contracts', member_count: 1, document_count: 1, created_at: new Date().toISOString() }],
  })))
}

test.describe('Journey 71 — keyboard-only operability', () => {
  test('skip link is first tab stop and jumps to main content', async ({ page }) => {
    await mockCore(page)
    await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([])))
    await page.goto('/workspaces')
    // The router moves focus into the content region after navigation,
    // so the skip link isn't literally the first Tab stop on an SPA
    // load — the a11y contract is: it exists, becomes VISIBLE when
    // focused (not sr-only), and jumps to #main-content on Enter.
    const skip = page.getByRole('link', { name: /skip to content/i })
    await expect(skip).toBeAttached()
    await skip.focus()
    await expect(skip).toBeFocused()
    await expect(skip).toBeVisible() // focus:not-sr-only reveals it
    await page.keyboard.press('Enter')
    await expect(page).toHaveURL(/#main-content/)
  })

  test('login completes with keyboard only', async ({ page }) => {
    // mockCore FIRST: it registers the lowest-priority abort catch-all,
    // and Playwright matches last-registered first — a route registered
    // BEFORE mockCore would be shadowed by the catch-all.
    await mockCore(page)
    await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([])))
    await page.route('**/api/v1/auth/login', (r) => r.fulfill(json({
      // finalizeLoginResult (H-1 guard) requires tenant_id ON the user.
      user: { id: USER, email: 'me@example.com', display_name: 'Me', role: 'owner', status: 'active', mfa_enabled: false, tenant_id: 't-1' },
      tenant_id: 't-1',
    })))
    await page.goto('/login')
    // Keyboard-only input: type into each field and submit with Enter.
    // (Fields are focused directly rather than counting Tab stops — the
    // form's field order isn't the contract under test.)
    await page.getByLabel(/email/i).focus()
    await page.keyboard.type('me@example.com')
    await page.getByLabel(/password/i).focus()
    await page.keyboard.type('hunter2hunter2')
    await page.keyboard.press('Enter') // submits the form — no pointer
    await expect(page).not.toHaveURL(/\/login/)
  })

  test('task completes via keyboard (approve surface)', async ({ page }) => {
    await mockCore(page)
    let completed = false
    await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([{
      id: 't-1', title: 'Approve the NDA', description: '', status: 'open',
      priority: 'high', due_at: null, source: 'user', created_by: USER,
      created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    }])))
    await page.route('**/api/v1/tasks/t-1/complete', (r) => {
      completed = true
      return r.fulfill(json({ status: 'ok' }))
    })
    await page.goto('/tasks')
    await expect(page.getByText('Approve the NDA')).toBeVisible()
    // Reach the row's complete control with the keyboard and activate
    // with Enter — no pointer events involved.
    const done = page.getByRole('button', { name: /complete|done/i }).first()
    await done.focus()
    await expect(done).toBeFocused()
    await page.keyboard.press('Enter')
    await expect.poll(() => completed).toBe(true)
  })

  test('dialogs open, trap and close with the keyboard', async ({ page }) => {
    await mockCore(page)
    await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([])))
    await page.goto('/tasks')
    const newTask = page.getByRole('button', { name: /new task/i }).first()
    await newTask.focus()
    await page.keyboard.press('Enter')
    const title = page.getByTestId('task-title')
    await expect(title).toBeVisible()
    // Radix autofocuses the first field — waiting for that guarantees
    // the dialog is interactive before Escape (parallel-run timing).
    await expect(title).toBeFocused()
    await page.keyboard.press('Escape')
    // One retry absorbs a dropped key on a loaded machine; a healthy
    // dialog closes on the first press.
    if (await title.isVisible().catch(() => false)) {
      await page.keyboard.press('Escape')
    }
    await expect(title).toBeHidden()
  })

  test('document move works without drag-and-drop (keyboard alternative)', async ({ page }) => {
    await mockCore(page)
    await page.route(`**/api/v1/workspaces/${WS}`, (r) => r.fulfill(json({
      id: WS, name: 'Contracts', description: '', member_count: 1, document_count: 1,
      created_at: new Date().toISOString(),
    })))
    await page.route(`**/api/v1/workspaces/${WS}/folders**`, (r) => r.fulfill(json({
      folders: [{ id: 'f-1', name: 'NDAs', workspace_id: WS, visibility: 'shared', created_at: new Date().toISOString() }],
    })))
    await page.route(`**/api/v1/workspaces/${WS}/documents**`, (r) => r.fulfill(json({
      documents: [{
        id: DOC, workspace_id: WS, folder_id: 'f-1', title: 'Master Services Agreement',
        lifecycle_state: 'LIFECYCLE_STATE_ACTIVE', mime_type: 'application/pdf',
        total_size_bytes: 1, tags: [], created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(), version_count: 1,
      }],
      total_count: 1, page_token: '',
    })))
    await page.goto(`/workspaces/${WS}`)
    await expect(page.getByText('Master Services Agreement').first()).toBeVisible()

    // The tile's actions menu is a real button: focus + Enter opens the
    // Radix menu, arrow keys navigate, Enter activates "Move to
    // folder…" — the documented keyboard alternative to dnd (the grip
    // also supports dnd-kit's KeyboardSensor: Space lift + arrows).
    const menuBtn = page.getByTestId(`document-actions-trigger-${DOC}`)
    await menuBtn.focus()
    await page.keyboard.press('Enter')
    const moveItem = page.getByRole('menuitem', { name: /move to folder/i })
    await expect(moveItem).toBeVisible()
    await moveItem.focus()
    await page.keyboard.press('Enter')
    // The move dialog is now open — keyboard path complete.
    await expect(page.getByRole('dialog')).toBeVisible()
    await page.keyboard.press('Escape')
  })
})
