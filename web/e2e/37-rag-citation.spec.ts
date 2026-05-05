// ADR 0063 — workspace-scoped /ask journey. Mocks the intelligence
// /rag/query endpoint and asserts that the answer renders, inline
// citations are clickable, and thumbs feedback POSTs to the right
// URL. Pattern matches 36-redaction-review.spec.ts.

import { test, expect } from '@playwright/test'

const TENANT_ID    = '00000000-0000-0000-0000-000000000100'
const USER_ID      = '00000000-0000-0000-0000-000000000001'
const WORKSPACE_ID = '00000000-0000-0000-0000-000000000200'
const DOCUMENT_ID  = '00000000-0000-0000-0000-000000000def'
const QUERY_ID     = '00000000-0000-0000-0000-000000004001'

test.describe('Journey 37 — Workspace RAG (/ask)', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
            role: 'admin', status: 'active', mfa_enabled: false,
          },
          tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER_ID, email: 'alice@example.com', display_name: 'Alice',
          role: 'admin', tenant_id: TENANT_ID,
        }),
      }),
    )
    await page.route('**/api/v1/workspaces', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          workspaces: [{
            id: WORKSPACE_ID, name: 'Engineering',
            document_count: 5, member_count: 3,
            created_at: '2026-04-01T00:00:00Z',
          }],
        }),
      }),
    )

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('renders answer with clickable inline citations', async ({ page }) => {
    let postedBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/intelligence/rag/query', async (route) => {
      postedBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          query_id: QUERY_ID,
          answer: `The vendor agreement renews annually on the anniversary date. See [${DOCUMENT_ID}:page_3] for details.`,
          citations: [{
            doc_id: DOCUMENT_ID,
            workspace_id: WORKSPACE_ID,
            page: 3, chunk_id: 12,
            section_path: 'Article 5 / Section 5.2',
            snippet: 'The vendor agreement shall renew annually …',
            score: 0.91,
          }],
          model: 'anthropic/claude-haiku-4-5',
          input_tokens: 850, output_tokens: 64, cost_usd: 0.001, elapsed_ms: 420,
        }),
      })
    })

    await page.goto('/ask')
    await page.getByTestId('ask-question-input').fill('When does the vendor agreement renew?')
    await page.getByTestId('ask-submit').click()

    // Answer rendered.
    await expect(page.getByTestId('ask-answer')).toBeVisible()

    // Inline citation link points to the doc detail page with the
    // citation's workspace + doc id, not the page-state workspace.
    const inline = page.getByTestId('ask-inline-citation')
    await expect(inline).toBeVisible()
    await expect(inline).toHaveAttribute(
      'href',
      `/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`,
    )

    // Sources list surfaces the section path breadcrumb.
    await expect(page.getByTestId('ask-citations-list')).toContainText('Article 5 / Section 5.2')

    // Body posted to the backend includes the workspace selector
    // value and the question, not the model field (default).
    await expect.poll(() => postedBody).toMatchObject({
      question: 'When does the vendor agreement renew?',
    })
  })

  test('thumbs-up POSTs to /feedback with up', async ({ page }) => {
    await page.route('**/api/v1/intelligence/rag/query', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          query_id: QUERY_ID,
          answer: 'Annual renewal.',
          citations: [],
          model: 'anthropic/claude-haiku-4-5',
          input_tokens: 10, output_tokens: 4, cost_usd: 0, elapsed_ms: 100,
        }),
      }),
    )
    let feedbackBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/intelligence/rag/query/${QUERY_ID}/feedback`, async (route) => {
      feedbackBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ status: 'recorded' }),
      })
    })

    await page.goto('/ask')
    await page.getByTestId('ask-question-input').fill('Anything?')
    await page.getByTestId('ask-submit').click()

    await page.getByTestId('ask-feedback-up').click()
    await expect.poll(() => feedbackBody).toMatchObject({ feedback: 'up' })
    // After feedback recorded, the up/down buttons disable to prevent
    // double-fires (one feedback per query).
    await expect(page.getByTestId('ask-feedback-up')).toBeDisabled()
    await expect(page.getByTestId('ask-feedback-down')).toBeDisabled()
  })

  test('hides feedback buttons on the "I don\'t know." sentinel response', async ({ page }) => {
    await page.route('**/api/v1/intelligence/rag/query', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          query_id: QUERY_ID,
          answer: "I don't know.",
          citations: [],
          model: 'anthropic/claude-haiku-4-5',
          input_tokens: 0, output_tokens: 0, cost_usd: 0, elapsed_ms: 50,
        }),
      }),
    )

    await page.goto('/ask')
    await page.getByTestId('ask-question-input').fill('Something nobody indexed')
    await page.getByTestId('ask-submit').click()

    await expect(page.getByTestId('ask-answer')).toContainText("I don't know.")
    await expect(page.getByTestId('ask-feedback-up')).toHaveCount(0)
  })

  test('429 quota response surfaces toast', async ({ page }) => {
    await page.route('**/api/v1/intelligence/rag/query', (route) =>
      route.fulfill({
        status: 429,
        contentType: 'application/json',
        body: JSON.stringify({ detail: 'daily rag query limit reached (200)' }),
      }),
    )

    await page.goto('/ask')
    await page.getByTestId('ask-question-input').fill('Anything?')
    await page.getByTestId('ask-submit').click()

    // The axios interceptor surfaces a "Rate limited" toast for 429.
    await expect(page.getByText(/rate limited/i)).toBeVisible()
  })
})
