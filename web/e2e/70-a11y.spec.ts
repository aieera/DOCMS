// A11y gate (ADR 0120) — axe-core WCAG 2.1 A/AA scans over the core
// flows (login → browse → search → view → tasks). Runs in CI next to
// the smoke spec; ANY violation fails, so regressions can't land.
//
// Pattern: same fully-mocked backend as every other spec (page.route);
// pages are scanned POPULATED (real rows, open dialogs) because empty
// states hide most labelling/contrast bugs.

import { test, expect, type Page } from '@playwright/test'
import AxeBuilder from '@axe-core/playwright'

const TENANT = 't-1'
const USER = 'u-1'
const WS = '00000000-0000-0000-0000-00000000aaaa'
const DOC = '00000000-0000-0000-0000-00000000bbbb'

async function scan(page: Page, context: string) {
  const results = await new AxeBuilder({ page })
    .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
    .analyze()
  const summary = results.violations
    .map((v) => `${v.id} [${v.impact}] ${v.help}\n${v.nodes
      .map((n) => `    ${JSON.stringify(n.target)} — ${n.failureSummary?.split('\n')[1] ?? ''}`)
      .join('\n')}`)
    .join('\n')
  expect(results.violations, `axe violations on ${context}:\n${summary}`).toEqual([])
}

function json(body: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

// Baseline mocks so authed pages render without network errors.
async function mockCore(page: Page) {
  // LOWEST-priority catch-all (Playwright matches last-registered
  // first): any API call the test forgot to mock is ABORTED instead of
  // reaching the real gateway a dev machine may have running — a live
  // 401 there triggers the app's logout-and-redirect and poisons the
  // test at whatever moment the poll fires (flaky by timing).
  await page.route('**/api/v1/**', (r) => r.abort())
  await page.route('**/api/v1/notifications', (r) => r.fulfill(json({ items: [], total_count: 0 })))
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(json({
    id: USER, email: 'me@example.com', display_name: 'Me',
    role: 'owner', tenant_id: TENANT, tenant_slug: 'demo',
  })))
  await page.route('**/api/v1/notifications/unread-count', (r) => r.fulfill(json({ count: 2 })))
  await page.route('**/api/v1/notifications?**', (r) => r.fulfill(json({ items: [], total_count: 0 })))
  await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/workflows/tasks**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/saved-searches**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/workspaces', (r) => r.fulfill(json({
    workspaces: [{ id: WS, name: 'Contracts', description: 'Legal contracts', member_count: 3, document_count: 12, created_at: new Date().toISOString() }],
  })))
}

test.describe('Journey 70 — accessibility (axe AA)', () => {
  test('login page', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible()
    await scan(page, '/login')
  })

  test('workspaces home', async ({ page }) => {
    await mockCore(page)
    await page.goto('/workspaces')
    await expect(page.getByText('Contracts').first()).toBeVisible()
    await scan(page, '/workspaces')
  })

  test('search with results', async ({ page }) => {
    await mockCore(page)
    await page.route('**/api/v1/search', (r) => r.fulfill(json({
      results: [{
        document_id: DOC, title: 'Master Services Agreement', workspace_id: WS,
        lifecycle_state: 'active', tags: ['contract'], created_by_name: 'Alice',
        created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
        size_bytes: 12345, mime_type: 'application/pdf', version_count: 3, score: 1,
      }],
      total_count: 1, facets: {}, latency_ms: 5, search_mode: 'keyword',
    })))
    await page.route('**/api/v1/search/**', (r) => r.fulfill(json({ documents: [], tags: [], people: [], recent: [] })))
    await page.goto('/search?q=agreement')
    await expect(page.getByText('Master Services Agreement')).toBeVisible()
    await scan(page, '/search')
  })

  test('tasks inbox', async ({ page }) => {
    await mockCore(page)
    await page.unroute('**/api/v1/tasks/mine**')
    await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([{
      id: 't-1', title: 'Review the NDA', description: 'Second pass',
      status: 'open', priority: 'high', due_at: null, source: 'user',
      created_by: USER, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
    }])))
    await page.goto('/tasks')
    await expect(page.getByText('Review the NDA')).toBeVisible()
    await scan(page, '/tasks')
  })

  test('saved searches', async ({ page }) => {
    await mockCore(page)
    await page.unroute('**/api/v1/saved-searches**')
    await page.route('**/api/v1/saved-searches**', (r) => r.fulfill(json([{
      id: 's-1', name: 'Recent contracts', query: 'contract', filters: {},
      notify: true, notify_interval_minutes: 15, alert_frequency_cron: '',
      created_at: new Date().toISOString(), subscribers: [],
    }])))
    await page.goto('/saved-searches')
    await expect(page.getByText('Recent contracts').first()).toBeVisible()
    await scan(page, '/saved-searches')
  })

  test('workspace browse (docs + folders grid)', async ({ page }) => {
    await mockCore(page)
    await page.route(`**/api/v1/workspaces/${WS}`, (r) => r.fulfill(json({
      id: WS, name: 'Contracts', description: 'Legal contracts', member_count: 3,
      document_count: 1, created_at: new Date().toISOString(),
    })))
    await page.route(`**/api/v1/workspaces/${WS}/folders**`, (r) => r.fulfill(json({
      folders: [{ id: 'f-1', name: 'NDAs', workspace_id: WS, visibility: 'shared', created_at: new Date().toISOString() }],
    })))
    await page.route(`**/api/v1/workspaces/${WS}/documents**`, (r) => r.fulfill(json({
      documents: [{
        id: DOC, workspace_id: WS, folder_id: 'f-1', title: 'Master Services Agreement',
        lifecycle_state: 'LIFECYCLE_STATE_ACTIVE', mime_type: 'application/pdf',
        total_size_bytes: 12345, tags: ['contract'], created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(), version_count: 3,
      }],
      total_count: 1,
      page_token: '',
    })))
    await page.goto(`/workspaces/${WS}`)
    await expect(page.getByText('Master Services Agreement').first()).toBeVisible()
    await scan(page, '/workspaces/:id browse')
  })

  test('templates gallery + editor dialog', async ({ page }) => {
    await mockCore(page)
    await page.route('**/api/v1/templates', (r) => r.fulfill(json({
      templates: [{
        id: 'tpl-1', name: 'Project kit', description: 'Standard project tree',
        definition: { nodes: [{ name: 'Project {{project_name}}', docs: [{ title: 'Charter' }], children: [{ name: 'Contracts' }] }] },
        created_by: USER, created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
      }],
    })))
    await page.goto('/templates')
    await expect(page.getByText('Project kit')).toBeVisible()
    await scan(page, '/templates')
    // The tree editor dialog is part of the core authoring flow.
    await page.getByRole('button', { name: /edit/i }).first().click()
    await expect(page.getByTestId('template-name')).toBeVisible()
    await scan(page, '/templates editor dialog')
  })

  test('reports builder with results', async ({ page }) => {
    await mockCore(page)
    await page.route('**/api/v1/analytics/datasets', (r) => r.fulfill(json({
      datasets: [{ name: 'documents', dimensions: ['document_class', 'lifecycle_state'], measures: ['count'] }],
    })))
    await page.route('**/api/v1/analytics/reports', (r) => r.fulfill(json({ reports: [] })))
    await page.route('**/api/v1/analytics/query', (r) => r.fulfill(json({
      columns: ['document_class', 'count'],
      rows: [['contract', 42], ['invoice', 17]],
    })))
    await page.goto('/reports')
    await expect(page.getByTestId('run-query')).toBeVisible()
    await page.getByTestId('run-query').click()
    await expect(page.getByTestId('result-table')).toBeVisible()
    await scan(page, '/reports with results')
  })
})
