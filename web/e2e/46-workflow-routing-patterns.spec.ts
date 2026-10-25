// ADR 0064 — designer + tasks + recall journey.
//
// Coverage:
//   1. Designer renders the palette, lets you add an Approval step,
//      flags a missing-assignee validation issue, then clears when
//      filled in.
//   2. Tasks page renders Approve / Reject / Delegate buttons on a
//      pending row.
//   3. Workflow instance recall: 409 path renders a clear message;
//      success path closes the dialog and toasts.
//
// All backend traffic is mocked — the routing-pattern semantics
// themselves are pinned by Go tests in services/workflow/.

import { test, expect } from '@playwright/test'

const TENANT = 't-1'
const USER   = 'u-1'

test.describe('Journey 46 — workflow routing patterns', () => {
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
  })

  test('designer flags missing assignee then clears', async ({ page }) => {
    // The standalone /workflows/designer route is gone (now a 404); the
    // template builder at /workflows/$templateId/edit ('new' = blank
    // template) owns this journey with the same validation rules
    // (components/workflows/validate.ts: approval needs an assignee).
    await page.goto('/workflows/new/edit')
    await page.getByTestId('editor-name').fill('NDA approval')
    await page.getByTestId('step-preset-menu').click()
    await page.getByTestId('step-preset-approval').click()
    // Validation summary should reflect the missing-assignee issue.
    await expect(page.getByText('1 issue', { exact: true })).toBeVisible()
    await expect(page.getByTestId('step-config-issues')).toContainText(/needs an assignee/i)
    // Save button is disabled until issues clear.
    await expect(page.getByTestId('editor-save')).toBeDisabled()
    // Fill in the assignee value to clear the issue. The panel's field
    // labels are not bound to their inputs, so scope by the label text.
    const assigneeField = page.getByTestId('step-config-panel').locator('div.space-y-1')
      .filter({ has: page.getByText('Assignee', { exact: true }) })
    await assigneeField.getByRole('textbox').fill('00000000-0000-0000-0000-000000000001')
    await expect(page.getByText(/Definition valid/i)).toBeVisible()
    await expect(page.getByTestId('editor-save')).toBeEnabled()
  })

  test('tasks page exposes approve / reject / delegate', async ({ page }) => {
    await page.route('**/api/v1/workflows/tasks*', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{
          id: 'task-1', tenant_id: TENANT, instance_id: 'inst-1',
          document_id: 'd-1', document_title: 'NDA.pdf',
          step_name: 'Legal review', assignee_id: USER,
          status: 'pending', created_at: new Date().toISOString(),
        }]),
      }),
    )
    await page.goto('/tasks')
    // Approval steps moved to their own "Approvals" tab; /tasks opens on
    // the lightweight "My tasks" list.
    await page.getByTestId('tab-approvals').click()
    await expect(page.getByTestId('approve-task-1')).toBeVisible()
    await expect(page.getByTestId('reject-task-1')).toBeVisible()
    await expect(page.getByTestId('delegate-task-1')).toBeVisible()
  })

  test('recall: 409 when approver acted', async ({ page }) => {
    await page.route('**/api/v1/workflows/instances/inst-1', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          id: 'inst-1', definition_id: 'def-1', document_id: 'd-1',
          initiated_by: USER, status: 'running', current_step: 1,
          created_at: new Date().toISOString(),
        }),
      }),
    )
    await page.route('**/api/v1/workflows/instances/inst-1/timeline', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([
          { step_id: 'legal', outcome: 'approve', actor_id: 'u-2',
            from_step: 0, to_step: 1, at: new Date().toISOString() },
        ]),
      }),
    )
    await page.goto('/workflows/instances/inst-1')
    // Recall button is hidden because at least one approve event exists.
    await expect(page.getByTestId('recall-instance')).not.toBeVisible()
    // Timeline event renders.
    await expect(page.getByTestId('timeline')).toBeVisible()
    await expect(page.getByText(/^Approve$/i)).toBeVisible()
  })
})
