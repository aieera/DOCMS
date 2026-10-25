// ADR 0080 — /ask journey. The page is now the streaming chat over
// POST /intelligence/qa (SSE); the first test pins that contract (answer
// renders, citation chips link to the cited document). The original
// /intelligence/rag/query feedback + quota tests are fixme'd below — that
// UI no longer exists. Pattern matches 36-redaction-review.spec.ts.

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
            tenant_id: TENANT_ID,
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
    await page.getByLabel('Password', { exact: true }).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  })

  test('renders answer with clickable inline citations', async ({ page }) => {
    // /ask is now the streaming chat (POST /intelligence/qa, SSE frames:
    // conversation → citations → chunk → done) that replaced the one-shot
    // /intelligence/rag/query page. Citations render as chips under the
    // answer; a chip links to the cited document using the citation's own
    // workspace_id, not the page's scope.
    const citation = {
      chunk_index: 12,
      text: 'The vendor agreement shall renew annually …',
      page: 3,
      similarity_score: 0.91,
      document_id: DOCUMENT_ID,
      document_title: 'Vendor Agreement.pdf',
      version_id: '00000000-0000-0000-0000-000000000abc',
      marker: 1,
      workspace_id: WORKSPACE_ID,
      section_path: 'Article 5 / Section 5.2',
    }
    let postedBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/intelligence/qa/history**', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ conversations: [] }) }),
    )
    await page.route('**/api/v1/intelligence/qa', async (route) => {
      postedBody = JSON.parse(route.request().postData() ?? '{}')
      const frames = [
        { type: 'conversation', conversation_id: QUERY_ID },
        { type: 'citations', citations: [citation] },
        { type: 'chunk', text: 'The vendor agreement renews annually ' },
        {
          type: 'done',
          full_text: 'The vendor agreement renews annually on the anniversary date [1].',
          citations: [citation],
          model: 'anthropic/claude-haiku-4-5',
          input_tokens: 850, output_tokens: 64, cost_usd: 0.001, elapsed_ms: 420,
        },
      ]
      await route.fulfill({
        status: 200,
        contentType: 'text/event-stream',
        body: frames.map((f) => `data: ${JSON.stringify(f)}\n\n`).join(''),
      })
    })

    await page.goto('/ask')
    await page.getByTestId('ask-input').fill('When does the vendor agreement renew?')
    await page.getByTestId('ask-send').click()

    // Answer rendered.
    const answer = page.getByTestId('ask-msg-assistant')
    await expect(answer).toContainText('renews annually on the anniversary date')

    // Citation chip points to the doc detail page with the citation's
    // workspace + doc id, and names the cited page.
    const chip = answer.getByRole('link', { name: /Vendor Agreement\.pdf/ })
    await expect(chip).toBeVisible()
    await expect(chip).toHaveAttribute('href', `/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)
    await expect(chip).toContainText('p.3')

    // Body posted to the backend carries the question; "All workspaces"
    // (the default scope) asks globally.
    await expect.poll(() => postedBody).toMatchObject({
      question: 'When does the vendor agreement renew?',
      scope: 'global',
    })
  })

  // OBSOLETE (UI changed): the streaming /ask chat that replaced the
  // /intelligence/rag/query page has no per-answer thumbs feedback
  // (ask-feedback-up/down) — nothing in web/src posts to
  // /rag/query/{id}/feedback any more. Kept as fixme so the expectation is
  // visible if feedback comes back.
  test.fixme('thumbs-up POSTs to /feedback with up', async ({ page }) => {
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

  // OBSOLETE (UI changed): no feedback buttons exist in the streaming
  // /ask chat, so there is nothing to hide on the "I don't know." answer.
  test.fixme('hides feedback buttons on the "I don\'t know." sentinel response', async ({ page }) => {
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

  // OBSOLETE (UI changed): the chat streams through fetch(), not axios, so
  // a 429 no longer reaches the axios "Rate limited" interceptor — it
  // surfaces as a generic "qa stream failed: 429" toast. Whether quota
  // exhaustion should get dedicated copy again is a product call.
  test.fixme('429 quota response surfaces toast', async ({ page }) => {
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
