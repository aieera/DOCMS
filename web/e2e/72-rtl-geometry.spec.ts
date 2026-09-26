// RTL geometry gate — does the layout still hold when the UI mirrors?
//
// The existing RTL specs check that the shell flips (html[dir], sidebar
// on the right, modal copy text-start). None of them check that the
// flipped layout does not collide with itself, and that is where the
// real regression lived: the topbar's ⌘K chip climbed out of the search
// box and sat on the notification badges in Arabic, because the input
// was `flex-1` with no `min-w-0` and the leftover space lands on the
// other side under RTL.
//
// Method: render each page twice with IDENTICAL mock data, once LTR and
// once RTL, and report only what RTL broke. Measuring RTL alone lists
// every quirk English already has. See e2e/support/rtl-geometry.ts for
// why elements are keyed by DOM path and rects are clipped first.

import { test, expect, type Page } from '@playwright/test'

import { detectLayout, rtlOnly, type RtlFindings } from './support/rtl-geometry'

const TENANT = 't-1'
const USER = 'u-1'
const WS = '00000000-0000-0000-0000-00000000aaaa'
const DOC = '00000000-0000-0000-0000-00000000bbbb'

function json(body: unknown) {
  return { status: 200, contentType: 'application/json', body: JSON.stringify(body) }
}

// Which locale the mocked /auth/me reports. Mutable so a single page can
// be flipped between passes without re-registering routes.
let locale: 'en' | 'ar' = 'en'

async function mockApi(page: Page) {
  // Lowest priority (Playwright matches last-registered first): anything
  // unmocked is aborted rather than escaping to a real gateway.
  await page.route('**/api/v1/**', (r) => r.abort())
  await page.route('**/api/v1/auth/me', (r) => r.fulfill(json({
    id: USER, email: 'me@example.com', display_name: 'Me',
    role: 'owner', tenant_id: TENANT, tenant_slug: 'demo', locale,
  })))
  await page.route('**/api/v1/notifications/unread-count', (r) => r.fulfill(json({ count: 6 })))
  // Real rows, not an empty list: the notification row is the densest
  // thing in this gate (unread dot, icon, truncating title+body, relative
  // timestamp, hover actions) and an empty state exercises none of it.
  await page.route('**/api/v1/notifications**', (r) => r.fulfill(json({
    items: [
      { id: 'n-1', type: 'document.shared', read: false, created_at: '2026-09-25T08:30:00Z',
        title: 'Alice Administrator shared a document with you',
        body: 'Master Services Agreement — Northwind Traders (executed)' },
      { id: 'n-2', type: 'task.assigned', read: false, created_at: '2026-09-24T11:05:00Z',
        title: 'You were assigned a task', body: 'Countersign the renewal' },
      { id: 'n-3', type: 'workflow.completed', read: true, created_at: '2026-09-21T16:45:00Z',
        title: 'Approval workflow completed', body: 'Q3 board pack cleared all three reviewers' },
    ],
    total_count: 3,
  })))
  // Open tasks so the topbar's count badge actually renders. An empty
  // list leaves a gap next to the search box, and the regression this
  // gate exists for is the ⌘K chip landing ON that badge — with no
  // badge there is nothing to collide with and the sweep sees a clean
  // page. The mocked shell has to be as crowded as the real one.
  await page.route('**/api/v1/tasks/mine**', (r) => r.fulfill(json([
    { id: 'tk-1', title: 'Review the Northwind MSA', status: 'open', assignee_id: USER, created_at: new Date().toISOString() },
    { id: 'tk-2', title: 'Countersign the renewal', status: 'in_progress', assignee_id: USER, created_at: new Date().toISOString() },
  ])))
  await page.route('**/api/v1/workflows/tasks**', (r) => r.fulfill(json([])))
  // Devices & Sync — one live device and one revoked, so both badge
  // variants and a real timestamp are on the page to mirror.
  await page.route('**/api/v1/sync/devices**', (r) => r.fulfill(json({ devices: [
    { id: 'd-1', name: 'ops-laptop-01', platform: 'linux', last_seen_at: '2026-09-25T08:30:00Z',
      revoked: false, selective_folders: ['f-1', 'f-2'], cursor: 'eyJvZmZzZXQiOjQyfQ' },
    { id: 'd-2', name: 'finance-desktop-dublin', platform: 'windows', last_seen_at: null,
      revoked: true, selective_folders: [], cursor: '' },
  ] })))
  // Records & retention — a two-level file plan, because the tree indent
  // is the thing that has to mirror.
  await page.route('**/api/v1/records/schedules**', (r) => r.fulfill(json({ schedules: [
    { id: 's-1', name: 'Contracts — 7 year', trigger_event: 'declaration',
      retention_period_days: 2555, disposition_action: 'review' },
  ] })))
  await page.route('**/api/v1/records/categories**', (r) => r.fulfill(json({ categories: [
    { id: 'c-1', parent_id: null, name: 'Corporate', code: 'CORP', node_type: 'category' },
    { id: 'c-2', parent_id: 'c-1', name: 'Board minutes', code: 'CORP-BM', node_type: 'series',
      retention_schedule_id: 's-1' },
  ] })))
  await page.route('**/api/v1/records/disposition-queue**', (r) => r.fulfill(json({ records: [] })))
  await page.route('**/api/v1/admin/retention-policies**', (r) => r.fulfill(json([
    { id: 'p-1', name: 'Invoices — 7 years', description: 'Finance retention baseline',
      retain_days: 2555, then_action: 'archive', archive_days: 90, is_active: true,
      updated_at: '2026-09-20T09:00:00Z', document_class_filter: 'invoice', tag_filter: ['finance'] },
  ])))
  // Legal holds — the default tab of /admin/legal.
  await page.route('**/api/v1/compliance/holds**', (r) => r.fulfill(json([
    { id: 'h-1', name: 'Northwind v. Acme', matter_reference: '2026-CV-1183', is_active: true,
      description: 'All contract and correspondence records touching the Northwind matter.',
      applied_at: '2026-08-14T09:00:00Z', document_ids: ['d1', 'd2'] },
  ])))
  await page.route('**/api/v1/admin/ediscovery/jobs**', (r) => r.fulfill(json({ jobs: [] })))
  // --- Integrations: the eSign tab is the default one. getOrNull
  // surfaces treat null as "not configured", which is a valid state.
  await page.route('**/api/v1/connectors/google', (r) => r.fulfill(json(null)))
  await page.route('**/api/v1/admin/notifications/twilio', (r) => r.fulfill(json(null)))
  await page.route('**/api/v1/admin/notifications/smtp', (r) => r.fulfill(json({
    host: 'smtp.acme.example', port: 587, username: 'dms@acme.example', has_password: true,
    from_addr: 'dms@acme.example', starttls: true, updated_at: '2026-08-01T09:00:00Z' })))
  await page.route('**/api/v1/signatures/esign/connections', (r) => r.fulfill(json({ connections: [
    { id: 'c-1', provider: 'docusign', account_id: 'acme-legal', status: 'connected',
      expires_at: '2026-12-01T09:00:00Z' },
  ] })))
  await page.route('**/api/v1/signatures/esign/envelopes', (r) => r.fulfill(json({ envelopes: [] })))
  // --- AI & models ---
  await page.route('**/api/v1/admin/tenant/llm-config', (r) => r.fulfill(json({
    provider: 'anthropic', model: 'claude-opus-5-5', fallback_model: 'claude-haiku-4-5-20251001',
    base_url: null, rate_limit_rpm: 600, daily_budget_usd: 250, air_gapped: false,
    key_set: true, key_set_at: '2026-07-11T09:00:00Z', updated_at: '2026-09-01T09:00:00Z' })))
  await page.route('**/api/v1/admin/llm-usage**', (r) => r.fulfill(json({
    tenant_id: TENANT,
    by_model: [{ model: 'claude-opus-5-5', calls: 12840, input_tokens: 9120334,
                 output_tokens: 1204221, cost_usd: 184.22 }],
    totals: { calls: 12840, input_tokens: 9120334, output_tokens: 1204221, cost_usd: 184.22 } })))
  // --- Tagging (catalog is the default tab) ---
  await page.route('**/api/v1/tags**', (r) => r.fulfill(json({ tags: [
    { id: 'tg-1', name: 'contract', document_count: 412 },
    { id: 'tg-2', name: 'invoice', document_count: 1880 },
  ], total: 2 })))
  await page.route('**/api/v1/admin/auto-tag-config', (r) => r.fulfill(json({
    enabled: true, auto_apply_threshold: 0.85, suggest_threshold: 0.6,
    max_tags_per_document: 8, blocked_tags: ['misc'],
    source_weights: { ner: 1, classification: 0.8, llm: 0.9, pattern: 0.6 } })))
  // --- OCR ---
  await page.route('**/api/v1/admin/ocr-quality/config', (r) => r.fulfill(json({
    enabled: true, review_threshold: 0.7, excellent_threshold: 0.95, good_threshold: 0.85,
    fair_threshold: 0.7, auto_retry_below: 0.5, notify_on_poor: true })))
  await page.route('**/api/v1/admin/ocr-quality/stats', (r) => r.fulfill(json({
    total_documents: 18422, documents_by_grade: { excellent: 12044, good: 4120, fair: 1802, poor: 456 },
    open_review_pages: 318, auto_retried_documents: 212 })))
  await page.route('**/api/v1/intelligence/ocr/engine-config', (r) => r.fulfill(json({
    engine: 'auto', doc_type_overrides: { invoice: 'tesseract' } })))
  // --- Ingestion ---
  await page.route('**/api/v1/review-queue**', (r) => r.fulfill(json({ items: [
    { id: 'rq-1', ingestion_item_id: 'ii-1', workspace_id: WS, target_customer_ref: 'ACME-00182',
      document_class: 'invoice', extracted_external_key: 'INV-2026-4471', confidence: 0.62,
      reason: 'low_confidence', status: 'pending' },
  ] })))
  await page.route('**/api/v1/ingest/items**', (r) => r.fulfill(json({ items: [] })))
  // --- PII / PHI (findings is the default tab) ---
  await page.route('**/api/v1/admin/compliance/dashboard**', (r) => r.fulfill(json({
    total_documents_scanned: 18422, documents_with_findings: 1204, open_findings: 318,
    auto_held_documents: 12,
    risk_distribution: { critical: 12, high: 96, medium: 410, low: 686 },
    top_entity_types: [{ entity_type: 'EMAIL', count: 820 }, { entity_type: 'SSN', count: 96 }] })))
  // --- Routing rules. A negative priority is deliberate: digits AND the
  // minus sign are both bidi-weak, so "-5" is exactly the value that
  // reorders to "5-" if the cell inherits the RTL paragraph direction.
  await page.route('**/api/v1/admin/routing-rules**', (r) => r.fulfill(json({ rules: [
    { id: 'rr-1', name: 'Invoices → Finance', description: '', category_key: 'invoice',
      target_folder_id: 'f-1', target_workspace_id: WS, priority: 10, enabled: true,
      created_by: USER, created_at: '2026-05-02T09:00:00Z', updated_at: '2026-09-01T09:00:00Z' },
    { id: 'rr-2', name: 'Signed contracts → Legal/Executed', description: '',
      category_key: 'contract', target_folder_id: 'f-2', target_workspace_id: WS,
      priority: -5, enabled: false, created_by: USER,
      created_at: '2026-05-02T09:00:00Z', updated_at: '2026-09-01T09:00:00Z' },
  ] })))
  await page.route('**/api/v1/admin/smart-routing-config**', (r) => r.fulfill(json({
    enabled: true, auto_move_threshold: 0.9, suggest_threshold: 0.6,
    max_suggestions: 3, learn_from_history: true, use_similarity: true })))
  await page.route('**/api/v1/admin/compliance/config**', (r) => r.fulfill(json({
    enabled: true, auto_hold_on_critical: true, notify_on_high: true,
    notify_roles: ['admin'], pii_entity_risk_overrides: { EMAIL: 'low' },
    phi_enabled: false, custom_patterns: [] })))
  // /trash was in this gate measuring an EMPTY state -- roughly 120
  // characters of "nothing here" copy, which mirrors trivially and told
  // us nothing. Give it real rows so the route earns its place.
  // /tasks reads the ENVELOPE endpoint (/tasks), not /tasks/mine which
  // the topbar badge uses -- so the page itself was measuring an empty
  // state while the badge above it had data.
  await page.route('**/api/v1/tasks?**', (r) => r.fulfill(json({
    items: [
      { id: 'tk-1', title: 'Review the Northwind MSA', status: 'open', priority: 'high',
        assignee_id: USER, assignee_name: 'Me', created_at: '2026-09-22T09:00:00Z',
        due_at: '2026-10-01T09:00:00Z', document_title: 'Master Services Agreement' },
      { id: 'tk-2', title: 'Countersign the renewal', status: 'in_progress', priority: 'normal',
        assignee_id: USER, assignee_name: 'Me', created_at: '2026-09-19T09:00:00Z' },
    ],
    total: 2, limit: 50, offset: 0,
  })))
  await page.route('**/api/v1/admin/trash/folders**', (r) => r.fulfill(json({ items: [] })))
  await page.route('**/api/v1/admin/trash**', (r) => r.fulfill(json({ items: [
    { id: 'tr-1', title: 'Master Services Agreement — Northwind Traders (executed)',
      workspace_id: WS, mime_type: 'application/pdf', total_size_bytes: 128934,
      lifecycle_state: 'LIFECYCLE_STATE_ACTIVE', deleted_by_name: 'Alice Administrator',
      deleted_at: '2026-09-20T09:00:00Z', user_cleared: true },
    { id: 'tr-2', title: 'Q3 board pack (draft)', workspace_id: WS,
      mime_type: 'application/pdf', total_size_bytes: 884120,
      lifecycle_state: 'LIFECYCLE_STATE_DRAFT', deleted_by_name: 'Bob Reviewer',
      deleted_at: '2026-09-18T14:30:00Z' },
  ] })))
  // Three KEK versions so the rotation history has rows to mirror, and a
  // key ARN long enough to be the thing that overflows if anything does.
  await page.route('**/api/v1/admin/encryption/status', (r) => r.fulfill(json({
    versions: [
      { version: 3, alias: 'kek-demo-v3', provider: 'aws_kms',
        external_key_ref: 'arn:aws:kms:eu-west-1:123456789012:key/8f2a1c34-9b7e-4d51-a0c8-1e6f5b3d9a27',
        created_at: '2026-09-01T09:00:00Z', active: true },
      { version: 2, alias: 'kek-demo-v2', provider: 'vault',
        external_key_ref: 'transit/keys/tenant-demo',
        created_at: '2026-05-14T09:00:00Z', retired_at: '2026-09-01T09:00:00Z', active: false },
      { version: 1, alias: 'kek-demo-v1', provider: 'local',
        created_at: '2025-01-04T09:00:00Z', revoked_at: '2026-05-14T09:00:00Z', active: false },
    ],
  })))
  await page.route('**/api/v1/saved-searches**', (r) => r.fulfill(json([])))
  await page.route('**/api/v1/workspaces', (r) => r.fulfill(json({
    workspaces: [{
      id: WS, name: 'Contracts & Commercial Agreements',
      description: 'Every executed agreement, indexed by counterparty.',
      member_count: 3, document_count: 12, created_at: new Date().toISOString(),
    }],
  })))
  await page.route(`**/api/v1/workspaces/${WS}`, (r) => r.fulfill(json({
    id: WS, name: 'Contracts & Commercial Agreements', member_count: 3,
    document_count: 1, created_at: new Date().toISOString(),
  })))
  await page.route(`**/api/v1/workspaces/${WS}/folders**`, (r) => r.fulfill(json({
    folders: [{ id: 'f-1', name: 'Non-disclosure agreements', workspace_id: WS, visibility: 'shared', created_at: new Date().toISOString() }],
  })))
  await page.route(`**/api/v1/workspaces/${WS}/documents**`, (r) => r.fulfill(json({
    documents: [{
      id: DOC, workspace_id: WS, folder_id: 'f-1',
      title: 'Master Services Agreement — Northwind Traders (executed)',
      lifecycle_state: 'LIFECYCLE_STATE_ACTIVE', mime_type: 'application/pdf',
      total_size_bytes: 128934, tags: ['contract', 'legal'],
      created_at: new Date().toISOString(), updated_at: new Date().toISOString(),
      version_count: 3, created_by_name: 'Alice Administrator',
    }],
    total_count: 1, page_token: '',
  })))
  await page.route('**/api/v1/search', (r) => r.fulfill(json({
    results: [{
      document_id: DOC, title: 'Master Services Agreement — Northwind Traders (executed)',
      workspace_id: WS, lifecycle_state: 'active', tags: ['contract'],
      created_by_name: 'Alice Administrator', created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(), size_bytes: 128934,
      mime_type: 'application/pdf', version_count: 3, score: 1,
    }],
    total_count: 1, facets: {}, latency_ms: 5, search_mode: 'keyword',
  })))
  await page.route('**/api/v1/search/**', (r) => r.fulfill(json({ documents: [], tags: [], people: [], recent: [] })))
}

async function setDirection(page: Page, next: 'en' | 'ar') {
  locale = next
  await page.context().clearCookies({ name: 'dms_locale' })
  await page.context().addCookies([
    { name: 'dms_locale', value: next, url: page.url().startsWith('http') ? page.url() : 'http://localhost:4173' },
  ])
  // Cookie only, deliberately: i18next's detection order is cookie >
  // localStorage > navigator, and addInitScript cannot be removed once
  // added, so seeding storage too would leave both passes' values armed.
}

// Layout under RTL is a TEXT-WIDTH effect, so nothing can be measured
// until the webfont that decides those widths has actually loaded. With
// the fallback font in place the topbar search box still fits and the
// regression this gate exists for reads as fixed.
async function settle(page: Page) {
  await page.evaluate(() => document.fonts.ready)
  await page.waitForTimeout(250)
}

async function measure(page: Page, route: string, dir: 'ltr' | 'rtl'): Promise<RtlFindings> {
  await page.goto(route)
  // Never measure a page that has not actually flipped — a silent
  // failure here would turn the whole gate green for the wrong reason.
  await page.waitForFunction((want) => document.documentElement.dir === want, dir, { timeout: 15_000 })
  await expect(page.locator('header[role="banner"]')).toBeVisible()
  // Arabic bundles arrive over HTTP; measuring mid-load compares Arabic
  // geometry against half-English text.
  await page.waitForLoadState('networkidle').catch(() => { /* best effort */ })
  await settle(page)
  // Nothing to measure passes the differential for the wrong reason --
  // that is exactly how this gate went vacuously green before. So prove
  // the page is really on screen before trusting a clean result from it.
  // The two ways it silently is not: the route error-boundaried, or the
  // session lapsed and we measured the login page 13 times. The
  // character floor is only a backstop for anything else; it is set
  // below the sparsest legitimate page here rather than at the density
  // we would like, so it never fails an honestly-empty state.
  const probe = await page.evaluate(() => {
    const main = document.querySelector('main') ?? document.body
    const text = main.innerText.trim()
    return { chars: text.length, boundary: /couldn.t be displayed/i.test(text) }
  })
  expect(probe.boundary, `${route} rendered an error boundary in ${dir}`).toBe(false)
  expect(page.url(), `${route} bounced to login in ${dir}`).not.toContain('/login')
  expect(probe.chars, `${route} rendered almost nothing in ${dir} — nothing to compare`)
    .toBeGreaterThan(120)
  return page.evaluate(detectLayout)
}

// Routes chosen for layout density rather than coverage: the shell is on
// every one of them, and these add a data table, a card grid, a filter
// bar and a settings form.
const ROUTES = ['/', '/search', `/workspaces/${WS}`, '/trash', '/tasks', '/admin', '/notifications', '/settings', '/settings/security', '/admin/tenant/encryption',
  '/admin/tenant/sync', '/admin/records-retention', '/admin/legal',
  '/admin/audit', '/admin/integrations', '/admin/ai', '/admin/tagging',
  '/admin/ocr', '/admin/ingestion', '/admin/pii-scanning',
  '/admin/intelligence/routing-rules']

for (const width of [1440, 768] as const) {
  test.describe(`RTL geometry @ ${width}px`, () => {
    test.use({ viewport: { width, height: 900 } })

    test('flipping to Arabic breaks no layout that English holds', async ({ page }) => {
      test.slow()
      await mockApi(page)

      const ltr: Record<string, RtlFindings> = {}
      await setDirection(page, 'en')
      for (const route of ROUTES) ltr[route] = await measure(page, route, 'ltr')

      await setDirection(page, 'ar')
      const broken: string[] = []
      for (const route of ROUTES) {
        const found = rtlOnly(ltr[route], await measure(page, route, 'rtl'))
        broken.push(...found.map((f) => `${route} — ${f}`))
      }

      expect(broken, `RTL-only layout breaks at ${width}px:\n  ${broken.join('\n  ')}`).toEqual([])
    })
  })
}

test.describe('RTL regressions pinned by name', () => {
  test.use({ viewport: { width: 1440, height: 900 } })

  test('the ⌘K chip stays inside the search box in Arabic', async ({ page }) => {
    // The original report: in Arabic the keyboard-shortcut chip sat on
    // top of the notification and task badges. The input was `flex-1`
    // with no `min-w-0`, so it refused to shrink below its intrinsic
    // width and pushed the chip out of the form.
    await mockApi(page)
    await setDirection(page, 'ar')
    await page.goto('/')
    await page.waitForFunction(() => document.documentElement.dir === 'rtl', null, { timeout: 15_000 })
    // dir flips on the <html> element before the shell mounts, so the
    // chip is not in the DOM yet at that point.
    await expect(page.locator('header[role="banner"] kbd')).toBeVisible()
    await settle(page)

    const box = await page.evaluate(() => {
      const header = document.querySelector('header[role="banner"]')
      const kbd = header?.querySelector('kbd')
      const form = kbd?.closest('form')
      if (!kbd || !form) return null
      const k = kbd.getBoundingClientRect()
      const f = form.getBoundingClientRect()
      return { escapesStart: k.left < f.left - 1, escapesEnd: k.right > f.right + 1, kw: k.width }
    })

    expect(box, 'topbar search form + kbd chip should both be present').not.toBeNull()
    expect(box!.kw).toBeGreaterThan(0)
    expect(box!.escapesStart, 'chip escaped the search box on the start edge').toBe(false)
    expect(box!.escapesEnd, 'chip escaped the search box on the end edge').toBe(false)
  })

  test('an English page title keeps its punctuation at the end under RTL', async ({ page }) => {
    // dir="auto" on PageHeader. Without it an untranslated English
    // description inside an RTL page renders as ".Manage tenant-wide
    // policy" — the bidi algorithm moves trailing punctuation to what it
    // believes is the end.
    await mockApi(page)
    await setDirection(page, 'ar')
    await page.goto('/trash')
    await page.waitForFunction(() => document.documentElement.dir === 'rtl', null, { timeout: 15_000 })
    await expect(page.locator('h1')).toBeVisible()
    await settle(page)

    const resolved = await page.evaluate(() => {
      const h = document.querySelector('h1')
      if (!h) return null
      // dir="auto" keys on the FIRST STRONG character, so ask the same
      // question: is the first letter that carries direction a Latin one?
      // If so the heading must resolve to ltr even though the page around
      // it is rtl.
      const strong = (h.textContent ?? '').match(/[A-Za-z\u0590-\u08FF]/)
      const latin = !!strong && /[A-Za-z]/.test(strong[0])
      return { latin, direction: getComputedStyle(h).direction, page: document.documentElement.dir }
    })

    expect(resolved, 'trash page should render an h1').not.toBeNull()
    expect(resolved!.page).toBe('rtl')
    if (resolved!.latin) {
      expect(resolved!.direction, 'Latin heading inside an RTL page must resolve to ltr').toBe('ltr')
    }
  })
})
