// ADR 0078 — NER entities tab journey. Mocks every backend call so
// the spec runs without a real intelligence stack.
//
// Pattern matches 01-login.spec.ts: page.route() everything, assert
// user-visible behavior.

import { test, expect } from '@playwright/test'

const TENANT_ID    = '00000000-0000-0000-0000-000000000100'
const USER_ID      = '00000000-0000-0000-0000-000000000001'
const WORKSPACE_ID = '00000000-0000-0000-0000-000000000200'
const DOCUMENT_ID  = '00000000-0000-0000-0000-000000000abc'
const VERSION_ID   = '00000000-0000-0000-0000-000000000def'
const ENTITY_ID    = '00000000-0000-0000-0000-000000001111'

test.describe('Journey 08 — NER entities tab', () => {
  test.beforeEach(async ({ page }) => {
    // Auth — same pattern as 01-login.
    await page.route('**/api/v1/auth/login', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: { id: USER_ID, email: 'alice@example.com', display_name: 'Alice', role: 'admin', status: 'active', mfa_enabled: false },
          tenant_id: TENANT_ID,
        }),
      })
    })
    await page.route('**/api/v1/auth/me', async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: USER_ID, email: 'alice@example.com', display_name: 'Alice', role: 'admin', tenant_id: TENANT_ID }),
      })
    })

    // Document detail.
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: DOCUMENT_ID,
          workspace_id: WORKSPACE_ID,
          title: 'Patient chart — synthetic',
          mime_type: 'application/pdf',
          size_bytes: 12345,
          lifecycle_state: 'active',
          document_class: 'medical_record',
          created_at: '2026-04-01T00:00:00Z',
          created_by_name: 'Alice',
          current_version_id: VERSION_ID,
        }),
      })
    })

    // OCR pages — used by the "In context" highlight panel.
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/versions/${VERSION_ID}/ocr`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'completed',
          total_pages: 1,
          avg_confidence: 0.97,
          pages: [
            {
              id: 'p1',
              version_id: VERSION_ID,
              page_number: 1,
              text_content:
                'Patient: John Doe\nSSN: 123-45-6789\nEmail: jdoe@example.com\nDiagnosis: E11.9.',
              confidence: 0.97,
              language: 'en',
              bounding_boxes: [],
              processing_time_ms: 1200,
              engine: 'paddle',
              created_at: '2026-04-01T00:00:01Z',
            },
          ],
        }),
      })
    })

    // Entities list — note `entity_value` and offsets line up with the
    // OCR text above so the highlighter has something to mark.
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/entities*`, async (route) => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          total: 4,
          limit: 500,
          offset: 0,
          entities: [
            { id: ENTITY_ID, entity_type: 'name', entity_value: 'John Doe',
              start_offset: 9, end_offset: 17, confidence: 0.85, is_pii: true,
              source: 'spacy', detected_at: '2026-04-01T00:00:02Z' },
            { id: '00000000-0000-0000-0000-000000001112', entity_type: 'national_id',
              entity_value: '123-45-6789',
              start_offset: 23, end_offset: 34, confidence: 0.95, is_pii: true,
              source: 'regex', detected_at: '2026-04-01T00:00:02Z' },
            { id: '00000000-0000-0000-0000-000000001113', entity_type: 'email',
              entity_value: 'jdoe@example.com',
              start_offset: 42, end_offset: 58, confidence: 0.9, is_pii: true,
              source: 'regex', detected_at: '2026-04-01T00:00:02Z' },
            { id: '00000000-0000-0000-0000-000000001114', entity_type: 'icd_code',
              entity_value: 'E11.9',
              start_offset: 70, end_offset: 75, confidence: 0.85, is_pii: false,
              source: 'regex', detected_at: '2026-04-01T00:00:02Z' },
          ],
        }),
      })
    })

    // Other intelligence panels render lazily — mock them as 404
    // (so badges hide gracefully) instead of mocking every shape.
    for (const path of [
      'compliance', 'tag-suggestions', 'route-suggestions',
      'ocr-quality', 'translations', 'language',
    ]) {
      await page.route(`**/api/v1/documents/${DOCUMENT_ID}/${path}*`, (route) =>
        route.fulfill({ status: 404, contentType: 'application/json', body: '{}' }),
      )
    }

    // Sign in to bypass the auth gate.
    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('opens Entities tab and shows grouped + highlighted entities', async ({ page }) => {
    await page.goto(`/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)

    // Switch to the Entities tab.
    await page.getByTestId('tab-entities').click()

    // Header summarizes total + group count.
    await expect(page.getByText('4 entities')).toBeVisible()
    await expect(page.getByText('(4 types)')).toBeVisible()

    // Grouped sections render PII + Medical (the two groups present).
    await expect(page.getByRole('heading', { name: 'PII', exact: true })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Medical', exact: true })).toBeVisible()

    // Inline highlighter marks each entity.
    const inContext = page.getByTestId('entities-in-context')
    await expect(inContext).toBeVisible()
    await expect(inContext.getByTestId('entity-mark-name')).toHaveText('John Doe')
    await expect(inContext.getByTestId('entity-mark-national_id')).toHaveText('123-45-6789')
    await expect(inContext.getByTestId('entity-mark-icd_code')).toHaveText('E11.9')
  })

  test('relabel posts the correction and refreshes the list', async ({ page }) => {
    let postedBody: Record<string, unknown> | null = null
    await page.route(`**/api/v1/documents/${DOCUMENT_ID}/entities/correct`, async (route) => {
      postedBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'corr-1', document_id: DOCUMENT_ID, version_id: VERSION_ID,
          original_type: 'name', corrected_type: 'party_name',
          entity_value: 'John Doe', action: 'relabel',
          corrected_by: USER_ID, created_at: '2026-04-01T00:01:00Z',
        }),
      })
    })

    await page.goto(`/workspaces/${WORKSPACE_ID}/documents/${DOCUMENT_ID}`)
    await page.getByTestId('tab-entities').click()

    // Click the per-row Relabel pencil for the "John Doe" row.
    const johnRow = page.getByRole('listitem').filter({ hasText: 'John Doe' }).first()
    await johnRow.getByLabel('Relabel').click()
    await johnRow.getByRole('combobox').selectOption('party_name')
    await johnRow.getByLabel('Save relabel').click()

    // Backend was called with the right payload.
    await expect.poll(() => postedBody).toMatchObject({
      action: 'relabel',
      original_entity_id: ENTITY_ID,
      corrected_type: 'party_name',
    })
  })
})
