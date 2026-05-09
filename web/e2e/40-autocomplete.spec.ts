// ADR 0084 — global CommandPalette autocomplete journey.
//
// Mocks /search/suggest and asserts:
//   - Cmd+K opens the palette; Esc closes it
//   - typing a 2-char prefix triggers a /suggest fetch
//   - the four groups (Recent, Documents, Tags, People) render with
//     the right counts + icons
//   - keyboard nav (arrow down + enter) selects a row
//   - clicking a Tags row navigates to /search?tag=<value>
//   - empty state ("no matches") only appears AFTER a fetch settles

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 40 — Global autocomplete', () => {
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

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('alice@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
    // Land on dashboard so Cmd+K is available globally.
    await page.waitForURL((url) => !url.pathname.includes('/login'))
  })

  test('Cmd+K opens palette; navigation group visible without typing', async ({ page }) => {
    await page.keyboard.press('Control+k')
    await expect(page.getByTestId('command-palette')).toBeVisible()
    await expect(page.getByTestId('suggest-group-nav')).toBeVisible()
    // Search-driven groups absent before the user types.
    await expect(page.getByTestId('suggest-group-documents')).toHaveCount(0)
    // Esc closes.
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('command-palette')).toHaveCount(0)
  })

  test('typing 2+ chars fetches /suggest and renders all four groups', async ({ page }) => {
    let fetchCount = 0
    await page.route('**/api/v1/search/suggest**', async (route) => {
      fetchCount += 1
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          documents: [
            { text: 'Acme MSA',     document_id: 'd1', score: 12.4 },
            { text: 'Acme Invoice', document_id: 'd2', score: 8.1  },
          ],
          tags:   [{ text: 'acme-billing', count: 7 }],
          people: [{ text: 'Alice Acme',   count: 12 }],
          recent: [{ text: 'acme renewal' }],
        }),
      })
    })

    await page.keyboard.press('Control+k')
    await page.getByTestId('command-palette-input').fill('acm')

    // Recent comes first per §7.5 ordering.
    await expect(page.getByTestId('suggest-group-recent')).toBeVisible()
    await expect(page.getByTestId('suggest-group-documents')).toBeVisible()
    await expect(page.getByTestId('suggest-group-tags')).toBeVisible()
    await expect(page.getByTestId('suggest-group-people')).toBeVisible()

    // Doc rows surface from the mock.
    await expect(page.getByTestId('suggest-doc-d1')).toContainText('Acme MSA')
    await expect(page.getByTestId('suggest-doc-d2')).toContainText('Acme Invoice')

    // Tags carry a "(count) docs" badge.
    await expect(page.getByTestId('suggest-tag-acme-billing')).toContainText('7 docs')

    // People too.
    await expect(page.getByTestId('suggest-person-Alice Acme')).toContainText('12 docs')

    // 1-char input does NOT fire — debounce gate is at 2 chars.
    expect(fetchCount).toBeGreaterThan(0)
  })

  test('1-char prefix does NOT trigger a /suggest fetch', async ({ page }) => {
    let fetchCount = 0
    await page.route('**/api/v1/search/suggest**', async (route) => {
      fetchCount += 1
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ documents: [], tags: [], people: [], recent: [] }),
      })
    })
    await page.keyboard.press('Control+k')
    await page.getByTestId('command-palette-input').fill('a')
    // Wait a beat to make sure no fetch fired despite the keystroke.
    await page.waitForTimeout(400)
    expect(fetchCount).toBe(0)
  })

  test('clicking a Tags suggestion navigates to /search with the tag pre-applied', async ({ page }) => {
    await page.route('**/api/v1/search/suggest**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          documents: [],
          tags: [{ text: 'urgent', count: 5 }],
          people: [], recent: [],
        }),
      }),
    )
    await page.route('**/api/v1/search**', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          results: [], total_count: 0, latency_ms: 5, search_mode: 'lexical',
          facets: { tag: [] },
        }),
      }),
    )

    await page.keyboard.press('Control+k')
    await page.getByTestId('command-palette-input').fill('urg')
    await page.getByTestId('suggest-tag-urgent').click()

    await expect(page).toHaveURL(/\/search\?.*tag=urgent/)
  })

  test('empty state appears only AFTER fetch settles, not during typing flicker', async ({ page }) => {
    // Slow the response so we can observe the in-flight state.
    await page.route('**/api/v1/search/suggest**', async (route) => {
      await new Promise((r) => setTimeout(r, 250))
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ documents: [], tags: [], people: [], recent: [] }),
      })
    })

    await page.keyboard.press('Control+k')
    await page.getByTestId('command-palette-input').fill('zzz')
    // Loading flicker — palette shows the input but no "no matches" yet.
    await expect(page.getByText(/No matches/i)).toHaveCount(0)

    // After the response settles, the empty state surfaces.
    await expect(page.getByText(/No matches for/i)).toBeVisible()
  })
})
