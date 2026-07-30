// Task journey (2026-07-28 task-service design; supersedes the ADR 0068
// single-assignee flow).
//
// Coverage:
//   1. /tasks renders the My-tasks tab with table + kanban toggle.
//   2. New-task dialog with TWO assignees and ONE document → POST /tasks.
//   3. Row opens the detail drawer showing both assignees and the doc.
//   4. A second assignee completes the task ("anyone completes it").
//   5. Commenting with an @mention posts the mention token.
//   6. Top-nav badge shows the open-task count.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER = 'u-1'
const OTHER = 'u-2'

const NOW = new Date().toISOString()

function task(over: Record<string, unknown> = {}) {
  return {
    id: 't-1',
    title: 'Send NDA to legal',
    description: 'Before Friday',
    status: 'open',
    priority: 'high',
    source: 'user',
    due_at: null,
    created_by: USER,
    created_at: NOW,
    updated_at: NOW,
    assignees: [{ user_id: USER, added_by: USER, added_at: NOW }],
    documents: [],
    ...over,
  }
}

function page1(items: unknown[]) {
  return JSON.stringify({ items, total: items.length, limit: 50, offset: 0 })
}

test.describe('Journey 50 — tasks', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'me@example.com',
          display_name: 'Me',
          role: 'owner',
          tenant_id: TENANT,
          tenant_slug: 'demo',
        }),
      }),
    )
    await page.route('**/api/v1/auth/users/directory**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          users: [
            { id: USER, display_name: 'Me', email: 'me@example.com' },
            { id: OTHER, display_name: 'Ada Lovelace', email: 'ada@example.com' },
          ],
        }),
      }),
    )
    await page.route('**/api/v1/workflows/tasks**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/tasks/mine**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
  })

  test('/tasks shows the My-tasks tab with table view + kanban toggle', async ({ page }) => {
    await page.route('**/api/v1/tasks?**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: page1([task()]) }),
    )

    await page.goto('/tasks')
    await expect(page.getByTestId('tab-my-tasks')).toBeVisible()
    await expect(page.getByTestId('tab-approvals')).toBeVisible()
    await expect(page.getByTestId('task-table')).toBeVisible()
    await expect(page.getByText('Send NDA to legal')).toBeVisible()

    await page.getByTestId('view-kanban').click()
    await expect(page.getByTestId('task-kanban')).toBeVisible()
    await expect(page.getByTestId('col-open')).toBeVisible()
  })

  test('creates a task with two assignees and a linked document', async ({ page }) => {
    let posted: Record<string, unknown> | null = null

    await page.route('**/api/v1/search/suggest**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          documents: [{ text: 'Supplier Agreement.pdf', document_id: 'd-1', score: 1 }],
          tags: [],
          people: [],
          recent: [],
        }),
      }),
    )
    await page.route('**/api/v1/tasks?**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: page1(posted ? [task({ id: 't-2', title: 'Brand-new' })] : []),
      }),
    )
    await page.route('**/api/v1/tasks', async (route) => {
      if (route.request().method() !== 'POST') return route.fallback()
      posted = route.request().postDataJSON()
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify(task({ id: 't-2', title: 'Brand-new' })),
      })
    })

    await page.goto('/tasks')
    await page.getByTestId('new-task').click()
    await expect(page.getByTestId('task-create-dialog')).toBeVisible()

    await page.getByTestId('task-title').fill('Brand-new')

    // Second assignee (the current user is pre-selected).
    await page.getByTestId('assignee-search').fill('ada')
    await page.getByTestId(`assignee-option-${OTHER}`).click()

    // Linked document.
    await page.getByTestId('document-search').fill('supplier')
    await page.getByTestId('document-option-d-1').click()

    await page.getByTestId('task-create-submit').click()

    await expect.poll(() => posted).not.toBeNull()
    expect(posted!.assignee_ids).toEqual([USER, OTHER])
    expect(posted!.document_ids).toEqual(['d-1'])
  })

  test('a row opens the drawer, and a second assignee can complete the task', async ({ page }) => {
    const shared = task({
      assignees: [
        { user_id: OTHER, added_by: OTHER, added_at: NOW },
        { user_id: USER, added_by: OTHER, added_at: NOW },
      ],
      documents: [
        {
          document_id: 'd-1',
          workspace_id: 'w-1',
          title: 'Supplier Agreement.pdf',
          linked_by: OTHER,
          linked_at: NOW,
        },
      ],
      created_by: OTHER, // someone else raised it — we are only an assignee
    })
    let completed = false

    await page.route('**/api/v1/tasks?**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: page1([shared]) }),
    )
    await page.route('**/api/v1/tasks/t-1', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...shared, status: completed ? 'done' : 'open' }),
      }),
    )
    await page.route('**/api/v1/tasks/t-1/comments', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
      }
      return route.fallback()
    })
    await page.route('**/api/v1/tasks/t-1/activity**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/tasks/t-1/complete', (route) => {
      completed = true
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...shared, status: 'done' }),
      })
    })

    await page.goto('/tasks')
    await page.getByTestId('task-open-t-1').click()

    const drawer = page.getByTestId('task-detail-drawer')
    await expect(drawer).toBeVisible()
    // Both assignees and the linked document are visible in the drawer.
    await expect(drawer.getByTestId(`assignee-chip-${USER}`)).toBeVisible()
    await expect(drawer.getByTestId(`assignee-chip-${OTHER}`)).toBeVisible()
    await expect(drawer.getByTestId('task-doc-link-d-1')).toBeVisible()

    // We did not create this task — being an assignee is enough to close it.
    await drawer.getByTestId('task-complete').click()
    await expect.poll(() => completed).toBe(true)
  })

  test('posts a comment carrying an @mention token', async ({ page }) => {
    let body: Record<string, unknown> | null = null
    const t = task()

    await page.route('**/api/v1/tasks?**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: page1([t]) }),
    )
    await page.route('**/api/v1/tasks/t-1', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(t) }),
    )
    await page.route('**/api/v1/tasks/t-1/activity**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/tasks/t-1/comments', async (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({ status: 200, contentType: 'application/json', body: '[]' })
      }
      body = route.request().postDataJSON()
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'c-1',
          task_id: 't-1',
          author_id: USER,
          body: (body as { body: string }).body,
          mentions: [OTHER],
          created_at: NOW,
          updated_at: NOW,
        }),
      })
    })

    await page.goto('/tasks')
    await page.getByTestId('task-open-t-1').click()

    const input = page.getByTestId('task-comment-input')
    await input.fill('please review @ada')
    // The autocomplete replaces the trailing @token with the wire format.
    // Pick Ada explicitly — the directory mock also returns the current
    // user, so "first result" would insert the wrong person.
    await page
      .getByTestId('task-comment-mentions')
      .getByRole('button', { name: /Ada Lovelace/ })
      .click()
    await input.pressSequentially('thanks')
    await page.getByRole('button', { name: /comment/i }).click()

    await expect.poll(() => body).not.toBeNull()
    expect(String((body as { body: string }).body)).toContain(`@[Ada Lovelace](${OTHER})`)
  })

  test('top-nav badge shows the open-task count', async ({ page }) => {
    // Asserted from /tasks rather than the dashboard: the topbar is on
    // every authenticated page, and the dashboard fans out to a dozen
    // unrelated endpoints whose 401s would trip the logout interceptor
    // and bounce this test to /login.
    await page.unroute('**/api/v1/tasks/mine**')
    await page.route('**/api/v1/tasks/mine**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          task({ id: 't-1', title: 'A' }),
          task({ id: 't-2', title: 'B', status: 'in_progress' }),
        ]),
      }),
    )
    await page.route('**/api/v1/tasks?**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: page1([]) }),
    )

    await page.goto('/tasks')
    await expect(page.getByTestId('my-tasks-badge')).toBeVisible()
    await expect(page.getByTestId('my-tasks-count')).toHaveText('2')
  })
})
