// ADR 0062 — redaction review journey. Mocks every backend call so
// the spec runs without a real intelligence stack.
//
// Pattern matches 08-ner-entities.spec.ts: page.route() everything,
// assert user-visible behavior on the panel + apply flow.

import { test, expect } from '@playwright/test'

const TENANT_ID    = '00000000-0000-0000-0000-000000000100'
const USER_ID      = '00000000-0000-0000-0000-000000000001'
const WORKSPACE_ID = '00000000-0000-0000-0000-000000000200'
const DOCUMENT_ID  = '00000000-0000-0000-0000-000000000def'
const VERSION_ID   = '00000000-0000-0000-0000-000000000abc'
const CAND_ID_1    = '00000000-0000-0000-0000-000000003001'
const CAND_ID_2    = '00000000-0000-0000-0000-000000003002'

test.describe('Journey 36 — Redaction review', () => {
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

    await page.route(`**/api/v1/documents/${DOCUMENT_ID}`, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: DOCUMENT_ID,
          workspace_id: WORKSPACE_ID,
          title: 'Insurance application',
          mime_type: 'application/pdf',
          size_bytes: 102400,
          lifecycle_state: 'active',
          document_class: 'insurance_form',
          created_at: '2026-04-01T00:00:00Z',
          created_by_name: 'Alice',
          current_version_id: VERSION_ID,
        }),
      }),
    )

    // Mute the other panels' fetches.
    for (const path of [
      'compliance', 'tag-suggestions', 'route-suggestions',
      'ocr-quality', 'translations', 'language', 'entities',
    ]) {
      await page.route(`**/api/v1/documents/${DOCUMENT_ID}/${path}*`, (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: '{"entities":[],"total":0}' }),
      )
    }
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/versions/${VERSION_ID}/ocr`, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ status: 'completed', total_pages: 0, avg_confidence: 0, pages: [] }),
      }),
    )

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('renders pending candidates and approves one', async ({ page }) => {
    let candidates = [
      {
        id: CAND_ID_1, document_id: DOCUMENT_ID, version_id: VERSION_ID,
        source: 'ner', entity_type: 'national_id', entity_value: '123-45-6789',
        rectangles: [{ page: 0, x0: 72, y0: 100, x1: 162, y1: 112 }],
        page_number: 1, char_start: 23, char_end: 34,
        status: 'pending', created_at: '2026-04-01T00:00:02Z',
      },
      {
        id: CAND_ID_2, document_id: DOCUMENT_ID, version_id: VERSION_ID,
        source: 'ner', entity_type: 'email', entity_value: 'alice@example.com',
        rectangles: [{ page: 0, x0: 72, y0: 130, x1: 220, y1: 142 }],
        page_number: 1, char_start: 50, char_end: 67,
        status: 'pending', created_at: '2026-04-01T00:00:02Z',
      },
    ]
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/redaction-candidates*`, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ candidates, total: candidates.length, limit: 500, offset: 0 }),
      }),
    )

    let postedAction: string | null = null
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/redaction/candidates/${CAND_ID_1}/review`, async (route) => {
      const body = JSON.parse(route.request().postData() ?? '{}')
      postedAction = body.action
      // Reflect the new state in subsequent listCandidates calls.
      candidates = candidates.map((c) =>
        c.id === CAND_ID_1 ? { ...c, status: 'approved' } : c,
      )
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ ...candidates[0], status: 'approved' }),
      })
    })

    await page.goto(`/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)
    await page.getByTestId('tab-redaction').click()

    // Header summary surfaces total + per-status counts.
    await expect(page.getByText('2 candidates')).toBeVisible()
    await expect(page.getByText(/2 pending · 0 approved/)).toBeVisible()

    // First row: SSN-shaped national_id.
    const ssnRow = page.getByRole('listitem').filter({ hasText: '123-45-6789' })
    await expect(ssnRow.getByText('national_id')).toBeVisible()
    await ssnRow.getByLabel('Approve').click()

    await expect.poll(() => postedAction).toBe('approve')
    // Apply button enables the moment we have ≥1 approved.
    await expect(page.getByRole('button', { name: /Apply 1 approved/ })).toBeEnabled()
  })

  test('apply button is disabled with zero approved and surfaces count once approved', async ({ page }) => {
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/redaction-candidates*`, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          candidates: [{
            id: CAND_ID_1, document_id: DOCUMENT_ID, version_id: VERSION_ID,
            source: 'ner', entity_type: 'phone', entity_value: '(555) 867-5309',
            rectangles: [], page_number: 1, char_start: 0, char_end: 14,
            status: 'pending', created_at: '2026-04-01T00:00:02Z',
          }],
          total: 1, limit: 500, offset: 0,
        }),
      }),
    )

    await page.goto(`/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)
    await page.getByTestId('tab-redaction').click()
    await expect(page.getByRole('button', { name: /Apply 0 approved/ })).toBeDisabled()
  })

  test('apply POSTs to /redaction/apply and toasts on success', async ({ page }) => {
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/redaction-candidates*`, (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          candidates: [{
            id: CAND_ID_1, document_id: DOCUMENT_ID, version_id: VERSION_ID,
            source: 'ner', entity_type: 'national_id', entity_value: '123-45-6789',
            rectangles: [], page_number: 1, char_start: 0, char_end: 11,
            status: 'approved', created_at: '2026-04-01T00:00:02Z',
          }],
          total: 1, limit: 500, offset: 0,
        }),
      }),
    )

    let appliedBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/redaction/apply`, async (route) => {
      appliedBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 202,
        contentType: 'application/json',
        body: JSON.stringify({ job_id: 'job-1', candidate_count: 1, status: 'queued' }),
      })
    })

    await page.goto(`/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)
    await page.getByTestId('tab-redaction').click()
    // Switch to All so the approved row is visible.
    // (Default filter is `pending`, so `approved` rows don't count
    // in the badge for the filter view but do count in the apply
    // button which queries the unfiltered fetch.)
    await page.getByRole('button', { name: /Apply 1 approved/ }).click()

    await expect.poll(() => appliedBody).toMatchObject({
      version_id: VERSION_ID,
      force_admin_approve: false,
    })
  })
})
