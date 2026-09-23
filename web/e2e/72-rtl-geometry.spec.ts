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
  await page.route('**/api/v1/notifications**', (r) => r.fulfill(json({ items: [], total_count: 0 })))
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
  return page.evaluate(detectLayout)
}

// Routes chosen for layout density rather than coverage: the shell is on
// every one of them, and these add a data table, a card grid, a filter
// bar and a settings form.
const ROUTES = ['/', '/search', `/workspaces/${WS}`, '/trash', '/tasks', '/admin']

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
