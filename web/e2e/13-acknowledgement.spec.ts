// Journey 13 — Wave 15.1 acknowledgement create + acknowledge.
//
// Two flows in one spec:
//   (a) Admin creates a campaign at /admin/acknowledgements.
//   (b) User lands on /acknowledgements, clicks "I have read and
//       understood", and the pending row disappears.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 13 — acknowledgement', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'alice@example.com',
          display_name: 'Alice',
          role: 'compliance_officer',
          tenant_id: TENANT,
        }),
      }),
    )
  })

  test('admin creates a campaign', async ({ page }) => {
    const listCalls: string[] = []
    await page.route('**/api/v1/acknowledgement/campaigns**', (route) => {
      if (route.request().method() === 'POST') {
        listCalls.push('POST')
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({
            id: 'camp-1',
            tenant_id: TENANT,
            document_id: '00000000-0000-0000-0000-000000000abc',
            title: 'Annual Policy',
            due_at: '2026-05-06T12:00:00Z',
            status: 'active',
            created_by: USER,
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          }),
        })
      }
      listCalls.push('GET')
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ campaigns: [] }),
      })
    })

    await page.goto('/admin/acknowledgements')
    await page.getByRole('button', { name: /new campaign/i }).click()
    await page.getByLabel(/document id/i).fill('00000000-0000-0000-0000-000000000abc')
    await page.getByLabel(/^title$/i).fill('Annual Policy')
    await page.getByLabel(/recipient user ids/i).fill(USER)
    await page.getByRole('button', { name: /create & activate/i }).click()

    await expect.poll(() => listCalls.includes('POST')).toBe(true)
  })

  test('user acknowledges a pending assignment', async ({ page }) => {
    let pending = [
      {
        id: 'asgn-1',
        campaign_id: 'camp-1',
        assignee_user_id: USER,
        assigned_at: new Date().toISOString(),
        reminded_count: 0,
      },
    ]
    await page.route('**/api/v1/acknowledgement/my', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ pending }),
      }),
    )
    await page.route('**/api/v1/acknowledgement/assignments/asgn-1/ack', (route) => {
      pending = []
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'asgn-1',
          campaign_id: 'camp-1',
          assignee_user_id: USER,
          assigned_at: new Date().toISOString(),
          acknowledged_at: new Date().toISOString(),
          reminded_count: 0,
        }),
      })
    })

    await page.goto('/acknowledgements')
    await expect(page.getByText(/Campaign camp-1/i)).toBeVisible()

    await page.getByRole('button', { name: /I have read and understood/i }).click()
    // Inbox refetches after ack; pending goes to zero → empty state.
    await expect(page.getByText(/all caught up/i)).toBeVisible()
  })

  test('/acknowledgements inbox has no axe serious/critical violations', async ({ page }, testInfo) => {
    await page.route('**/api/v1/acknowledgement/my', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          pending: [
            {
              id: 'asgn-1',
              campaign_id: 'camp-1',
              assignee_user_id: USER,
              assigned_at: new Date().toISOString(),
              reminded_count: 2,
            },
          ],
        }),
      }),
    )
    await page.goto('/acknowledgements')
    await expect(page.getByText(/Campaign camp-1/i)).toBeVisible()
    await expectAxeClean(page, testInfo, 'acknowledgements-inbox')
  })

  test('/admin/acknowledgements has no axe serious/critical violations', async ({ page }, testInfo) => {
    await page.route('**/api/v1/acknowledgement/campaigns**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ campaigns: [] }),
      }),
    )
    await page.goto('/admin/acknowledgements')
    await expectAxeClean(page, testInfo, 'admin-acknowledgements')
  })
})
