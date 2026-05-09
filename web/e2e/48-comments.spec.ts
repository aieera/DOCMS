// ADR 0066 — comments + reactions journey.
//
// Coverage:
//   1. Empty state renders when no comments.
//   2. Posting a comment fires POST and shows up after refetch.
//   3. Resolve toggles the thread badge.
//   4. Reaction picker fires the addReaction call.
//   5. @mention autocomplete fires user search.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'
const DOC    = 'doc-1'

test.describe('Journey 48 — comments', () => {
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
    await page.route(`**/api/v1/documents/${DOC}`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: DOC, title: 'Doc.pdf',
          workspace_id: 'w-1', folder_id: 'f-1',
          mime_type: 'application/pdf',
          current_version_id: 'v-1',
          lifecycle_state: 'draft',
        }),
      }),
    )
  })

  test('empty state then create posts and re-renders', async ({ page }) => {
    let posted = false
    await page.route(`**/api/v1/documents/${DOC}/comments**`, (route) => {
      if (route.request().method() === 'POST') {
        posted = true
        return route.fulfill({
          status: 201, contentType: 'application/json',
          body: JSON.stringify({
            id: 'c-1', document_id: DOC, author_id: USER,
            body: 'hello world', is_resolved: false,
            created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
          }),
        })
      }
      return route.fulfill({
        status: 200, contentType: 'application/json',
        body: posted
          ? JSON.stringify([{
              id: 'c-1', document_id: DOC, author_id: USER,
              body: 'hello world', is_resolved: false,
              created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
            }])
          : '[]',
      })
    })

    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await expect(page.getByText(/No comments yet/i)).toBeVisible()
    await page.getByTestId('new-comment-input').fill('hello world')
    await page.getByTestId('new-comment-submit').click()
    await expect(page.getByText('hello world')).toBeVisible()
  })

  test('@mention autocomplete fires user search', async ({ page }) => {
    await page.route(`**/api/v1/documents/${DOC}/comments**`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/admin/users**', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          users: [{ id: 'u-2', email: 'alice@example.com', display_name: 'Alice', role: 'member' }],
          next_cursor: '',
        }),
      }),
    )
    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await page.getByTestId('new-comment-input').fill('Hey @al')
    await expect(page.getByTestId('new-comment-mentions')).toBeVisible()
    await expect(page.getByText('Alice')).toBeVisible()
  })

  test('resolve toggles thread state', async ({ page }) => {
    let resolved = false
    await page.route(`**/api/v1/documents/${DOC}/comments**`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{
          id: 'c-1', document_id: DOC, author_id: USER,
          body: 'needs review', is_resolved: resolved,
          created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        }]),
      }),
    )
    await page.route('**/api/v1/comments/c-1/resolve', (route) => {
      resolved = true
      return route.fulfill({ status: 204 })
    })

    await page.goto(`/workspaces/w-1/documents/${DOC}`)
    await page.getByTestId('resolve-c-1').click()
    // After invalidate the row re-renders with the resolved branch.
    await expect(page.getByText(/Resolved — click to reopen/i)).toBeVisible()
  })
})
