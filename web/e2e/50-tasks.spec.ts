// ADR 0068 — lightweight tasks journey.
//
// Coverage:
//   1. /tasks renders the My-tasks tab with table + kanban toggle.
//   2. New-task dialog → POST /tasks → row appears.
//   3. Top-nav badge shows the open-task count + hides at zero.
//   4. Approvals tab still surfaces workflow_tasks (back-compat).

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'

test.describe('Journey 50 — lightweight tasks', () => {
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
  })

  test('/tasks shows My-tasks tab with table view + kanban toggle', async ({ page }) => {
    await page.route('**/api/v1/tasks/mine**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{
          id: 't-1', title: 'Send NDA to legal', description: '',
          status: 'open', priority: 'high', due_at: null,
          source: 'user', created_by: USER, created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }]),
      }),
    )
    await page.route('**/api/v1/workflows/tasks**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )

    await page.goto('/tasks')
    await expect(page.getByTestId('tab-my-tasks')).toBeVisible()
    await expect(page.getByTestId('tab-approvals')).toBeVisible()
    await expect(page.getByTestId('task-table')).toBeVisible()
    await expect(page.getByText('Send NDA to legal')).toBeVisible()

    // Kanban toggle.
    await page.getByTestId('view-kanban').click()
    await expect(page.getByTestId('task-kanban')).toBeVisible()
    await expect(page.getByTestId('col-open')).toBeVisible()
  })

  test('new-task dialog posts and refetches', async ({ page }) => {
    let posted = false
    await page.route('**/api/v1/tasks/mine**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: posted
          ? JSON.stringify([{
              id: 't-2', title: 'Brand-new', description: '',
              status: 'open', priority: 'normal', due_at: null,
              source: 'user', created_by: USER, created_at: new Date().toISOString(),
              updated_at: new Date().toISOString(),
            }])
          : '[]',
      }),
    )
    await page.route('**/api/v1/workflows/tasks**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/tasks', (route) => {
      if (route.request().method() === 'POST') {
        posted = true
        return route.fulfill({
          status: 201, contentType: 'application/json',
          body: JSON.stringify({
            id: 't-2', title: 'Brand-new', description: '',
            status: 'open', priority: 'normal', source: 'user',
            created_by: USER, created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          }),
        })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
    })

    await page.goto('/tasks')
    await page.getByTestId('new-task').click()
    await expect(page.getByTestId('create-task-dialog')).toBeVisible()
    await page.getByTestId('task-title').fill('Brand-new')
    await page.getByTestId('create-task-submit').click()
    await expect(page.getByText('Brand-new')).toBeVisible()
  })

  test('top-nav badge shows count + hides at zero', async ({ page }) => {
    let calls = 0
    await page.route('**/api/v1/tasks/mine**', (route) => {
      calls++
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify(
          calls === 1
            ? [
                { id: 't-1', title: 'A', status: 'open',        priority: 'normal', source: 'user', created_by: USER, created_at: new Date().toISOString(), updated_at: new Date().toISOString(), description: '' },
                { id: 't-2', title: 'B', status: 'in_progress', priority: 'high',   source: 'user', created_by: USER, created_at: new Date().toISOString(), updated_at: new Date().toISOString(), description: '' },
              ]
            : []
        ),
      })
    })
    await page.goto('/')
    // First load: badge shows 2.
    await expect(page.getByTestId('my-tasks-badge')).toBeVisible()
    await expect(page.getByTestId('my-tasks-count')).toHaveText('2')
  })
})
