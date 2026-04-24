// Journey 02 — My Tasks approve.
//
// DoD from the Wave 7.4 wiring prompt:
//   "A reviewer logs in, sees their pending tasks, clicks approve,
//    the workflow advances, the task moves to 'completed'."
//
// Same patterns as 01-login: mock every backend call via page.route(),
// assert user-visible behaviour only.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'
const TASK_ID   = '10000000-0000-0000-0000-000000000001'
const INSTANCE  = '20000000-0000-0000-0000-000000000001'

const pendingTask = {
  id: TASK_ID,
  tenant_id: TENANT_ID,
  instance_id: INSTANCE,
  document_id: '30000000-0000-0000-0000-000000000001',
  document_title: 'Acme Master Services Agreement',
  step_name: 'Legal review',
  assignee_id: USER_ID,
  status: 'pending',
  created_at: new Date(Date.now() - 60_000).toISOString(),
}

const completedTask = { ...pendingTask, status: 'completed', completed_at: new Date().toISOString() }

test.describe('Journey 02 — My Tasks approve', () => {
  test.beforeEach(async ({ page }) => {
    // Reuse the 01-login mock surface so navigating via /login lands us
    // in the authenticated layout with a hydrated user.
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: { id: USER_ID, email: 'alice@example.com', display_name: 'Alice', role: 'admin', status: 'active', mfa_enabled: false },
          tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: USER_ID, email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: TENANT_ID }),
      }),
    )
  })

  test('approve advances the workflow and moves the task to completed', async ({ page }) => {
    // First GET /workflows/tasks?assignee=me&status=pending → one pending task.
    // After approve, the query refetches; serve an empty list (task is now
    // 'completed' so it drops out of the 'pending' filter).
    let tasksFetchCount = 0
    await page.route('**/api/v1/workflows/tasks*', (route) => {
      tasksFetchCount += 1
      const url = new URL(route.request().url())
      const status = url.searchParams.get('status')
      if (status === 'pending') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(tasksFetchCount === 1 ? [pendingTask] : []),
        })
      }
      if (status === 'completed') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify([completedTask]),
        })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    })

    // POST /workflows/instances/:id/signal — the decision send.
    let signalPayload: unknown = null
    await page.route(`**/api/v1/workflows/instances/${INSTANCE}/signal`, async (route) => {
      signalPayload = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'signaled' }),
      })
    })

    // Log in.
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
    await expect(page).toHaveURL('/')

    // Visit the tasks page; one pending task renders.
    await page.goto('/tasks')
    await expect(page.getByRole('heading', { name: /my tasks/i })).toBeVisible()
    await expect(page.getByText('Legal review')).toBeVisible()
    await expect(page.getByText('Acme Master Services Agreement')).toBeVisible()

    // Approve the task.
    await page.getByRole('button', { name: /approve/i }).click()

    // Signal fires with outcome=approve.
    await expect.poll(() => signalPayload).not.toBeNull()
    expect(signalPayload).toMatchObject({ outcome: 'approve', step_index: 0 })

    // List refetched and the pending list is now empty.
    await expect(page.getByText(/no pending tasks/i)).toBeVisible()

    // Switch to 'Completed' and confirm the task is there with status badge.
    await page.getByRole('button', { name: /^completed$/i }).click()
    await expect(page.getByText('Legal review')).toBeVisible()
  })
})
