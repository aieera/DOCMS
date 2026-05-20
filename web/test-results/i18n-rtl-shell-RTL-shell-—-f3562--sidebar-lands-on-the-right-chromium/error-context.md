# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: i18n-rtl-shell.spec.ts >> RTL shell — ADR 0107 >> html flips to rtl + lang=ar + sidebar lands on the right
- Location: e2e\i18n-rtl-shell.spec.ts:51:3

# Error details

```
Error: A snapshot doesn't exist at C:\Users\dell\Documents\DOCMS\web\e2e\i18n-rtl-shell.spec.ts-snapshots\rtl-dashboard-chromium-win32.png, writing actual.
```

# Page snapshot

```yaml
- generic [ref=e2]:
  - generic [ref=e3]:
    - complementary [ref=e4]:
      - generic [ref=e5]:
        - generic [ref=e6]:
          - link "VaultDMS" [ref=e7] [cursor=pointer]:
            - /url: /
            - img [ref=e9]
            - generic [ref=e13]: VaultDMS
          - button "Collapse sidebar" [ref=e14] [cursor=pointer]:
            - img
        - button "A Alice alice@example.com" [ref=e16] [cursor=pointer]:
          - generic [ref=e17]: A
          - generic [ref=e18]:
            - generic [ref=e19]: Alice
            - generic [ref=e20]: alice@example.com
          - img [ref=e21]
        - navigation "Primary" [ref=e24]:
          - generic [ref=e25]:
            - generic [ref=e26]: مساحة العمل
            - link "لوحة التحكم" [ref=e27] [cursor=pointer]:
              - /url: /
              - img [ref=e29]
              - generic [ref=e34]: لوحة التحكم
            - link "مساحات العمل" [ref=e35] [cursor=pointer]:
              - /url: /workspaces
              - img [ref=e36]
              - generic [ref=e38]: مساحات العمل
            - link "بحث" [ref=e39] [cursor=pointer]:
              - /url: /search
              - img [ref=e40]
              - generic [ref=e43]: بحث
            - link "اسأل" [ref=e44] [cursor=pointer]:
              - /url: /ask
              - img [ref=e45]
              - generic [ref=e47]: اسأل
            - link "عمليات البحث المحفوظة" [ref=e48] [cursor=pointer]:
              - /url: /saved-searches
              - img [ref=e49]
              - generic [ref=e51]: عمليات البحث المحفوظة
            - link "البنود" [ref=e52] [cursor=pointer]:
              - /url: /clauses
              - img [ref=e53]
              - generic [ref=e56]: البنود
          - generic [ref=e57]:
            - generic [ref=e58]: صندوق الوارد
            - link "المهام" [ref=e59] [cursor=pointer]:
              - /url: /tasks
              - img [ref=e60]
              - generic [ref=e63]: المهام
            - link "الإشعارات" [ref=e64] [cursor=pointer]:
              - /url: /notifications
              - img [ref=e65]
              - generic [ref=e68]: الإشعارات
            - link "المهملات" [ref=e69] [cursor=pointer]:
              - /url: /trash
              - img [ref=e70]
              - generic [ref=e73]: المهملات
          - generic [ref=e74]:
            - generic [ref=e75]: الإعدادات
            - link "إعداداتي" [ref=e76] [cursor=pointer]:
              - /url: /settings/security
              - img [ref=e77]
              - generic [ref=e89]: إعداداتي
            - link "الإدارة" [ref=e90] [cursor=pointer]:
              - /url: /admin
              - img [ref=e91]
              - generic [ref=e94]: الإدارة
    - generic [ref=e95]:
      - banner [ref=e96]:
        - navigation "Breadcrumb" [ref=e98]:
          - link "Home" [ref=e99] [cursor=pointer]:
            - /url: /
            - img [ref=e100]
            - generic [ref=e103]: Home
        - generic [ref=e104]:
          - button "بحث" [ref=e105] [cursor=pointer]:
            - img
            - generic [ref=e106]: ابحث في المستندات…
            - generic:
              - generic: ⌘
              - text: K
          - button "My tasks" [ref=e107] [cursor=pointer]:
            - img
          - button "Notifications" [ref=e108] [cursor=pointer]:
            - img
          - button "Toggle theme" [ref=e109] [cursor=pointer]:
            - img
          - combobox "اللغة" [ref=e110] [cursor=pointer]:
            - img [ref=e111]
            - generic: العربية
            - img [ref=e115]
          - button "Account menu" [ref=e117] [cursor=pointer]: A
      - main [ref=e118]:
        - generic [ref=e120]:
          - generic [ref=e122]:
            - heading "Good evening, Alice" [level=1] [ref=e123]
            - generic [ref=e124]:
              - text: Workspace overview ·
              - code [ref=e125]: "00000000"
          - generic [ref=e126]:
            - link "Workspaces 0 tenant total" [ref=e127] [cursor=pointer]:
              - /url: /workspaces
              - generic [ref=e128]:
                - generic [ref=e129]:
                  - img [ref=e131]
                  - img [ref=e133]
                - paragraph [ref=e135]: Workspaces
                - generic [ref=e136]: "0"
                - paragraph [ref=e137]: tenant total
            - link "Open tasks assigned to you" [ref=e138] [cursor=pointer]:
              - /url: /tasks
              - generic [ref=e139]:
                - generic [ref=e140]:
                  - img [ref=e142]
                  - img [ref=e145]
                - paragraph [ref=e147]: Open tasks
                - paragraph [ref=e150]: assigned to you
            - link "Unread notifications across all channels" [ref=e151] [cursor=pointer]:
              - /url: /notifications
              - generic [ref=e152]:
                - generic [ref=e153]:
                  - img [ref=e155]
                  - img [ref=e158]
                - paragraph [ref=e160]: Unread notifications
                - paragraph [ref=e163]: across all channels
          - region "Quick actions" [ref=e164]:
            - heading "Quick actions" [level=2] [ref=e165]
            - generic [ref=e166]:
              - link "Search documents ⌘K Full-text + semantic across the tenant" [ref=e167] [cursor=pointer]:
                - /url: /search
                - img [ref=e169]
                - generic [ref=e172]:
                  - generic [ref=e173]:
                    - paragraph [ref=e174]: Search documents
                    - generic: ⌘K
                  - paragraph [ref=e175]: Full-text + semantic across the tenant
              - link "Ask the corpus RAG over the documents you can see" [ref=e176] [cursor=pointer]:
                - /url: /ask
                - img [ref=e178]
                - generic [ref=e180]:
                  - paragraph [ref=e182]: Ask the corpus
                  - paragraph [ref=e183]: RAG over the documents you can see
              - link "Upload Drag a file into a workspace" [ref=e184] [cursor=pointer]:
                - /url: /workspaces
                - img [ref=e186]
                - generic [ref=e189]:
                  - paragraph [ref=e191]: Upload
                  - paragraph [ref=e192]: Drag a file into a workspace
              - link "Design a workflow Approval chains + signature steps" [ref=e193] [cursor=pointer]:
                - /url: /workflows/designer
                - img [ref=e195]
                - generic [ref=e199]:
                  - paragraph [ref=e201]: Design a workflow
                  - paragraph [ref=e202]: Approval chains + signature steps
          - generic [ref=e203]:
            - generic [ref=e205]:
              - generic [ref=e206]:
                - heading "My open tasks" [level=2] [ref=e207]
                - paragraph [ref=e208]: Approval steps + to-dos assigned to you.
              - button "View all" [ref=e209] [cursor=pointer]:
                - text: View all
                - img
            - generic [ref=e216]:
              - generic [ref=e217]:
                - heading "Recent activity" [level=2] [ref=e218]
                - paragraph [ref=e219]: Latest notifications across all channels.
              - button "View all" [ref=e220] [cursor=pointer]:
                - text: View all
                - img
  - region "Notifications alt+T"
```

# Test source

```ts
  1  | // ADR 0107 — RTL shell smoke spec.
  2  | //
  3  | // What we assert (the minimum that catches regressions):
  4  | //   1. ?lng=ar in the URL drives i18n into Arabic mode.
  5  | //   2. <html dir="rtl" lang="ar"> is applied before first paint.
  6  | //   3. The sidebar (data-testid="app-sidebar") visually lands on
  7  | //      the RIGHT side of the viewport — not just because of the
  8  | //      dir attribute but because the logical-utility migration
  9  | //      survived.
  10 | //   4. A screenshot is captured so visual diffs can catch
  11 | //      bidi-breakage we forgot to assert on.
  12 | //
  13 | // What we DON'T assert here:
  14 | //   * Specific translated strings — Prompt 4 owns the bundles.
  15 | //   * Pixel-perfect mirroring of every component — visual diff
  16 | //     is the right tool, not a Playwright text-content assertion.
  17 | //   * The PATCH /auth/me/locale wire — that needs a real backend
  18 | //     or extensive mocking; we cover it in unit tests instead.
  19 | 
  20 | import { test, expect } from '@playwright/test'
  21 | 
  22 | test.describe('RTL shell — ADR 0107', () => {
  23 |   test.beforeEach(async ({ page }) => {
  24 |     // Mock /auth/me so the authenticated layout mounts without a
  25 |     // real backend. locale='ar' so the auth-store sync path
  26 |     // confirms the server-side preference too.
  27 |     await page.route('**/api/v1/auth/me', async (route) => {
  28 |       await route.fulfill({
  29 |         status: 200,
  30 |         contentType: 'application/json',
  31 |         body: JSON.stringify({
  32 |           id:           '00000000-0000-0000-0000-000000000001',
  33 |           email:        'alice@example.com',
  34 |           display_name: 'Alice',
  35 |           role:         'admin',
  36 |           tenant_id:    '00000000-0000-0000-0000-000000000100',
  37 |           locale:       'ar',
  38 |           mfa_enabled:  false,
  39 |         }),
  40 |       })
  41 |     })
  42 | 
  43 |     // Empty workspace / docs / smart-folders so the layout renders
  44 |     // with structure but no list rows interfering with the bidi check.
  45 |     await page.route('**/api/v1/workspaces*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"workspaces":[]}' }))
  46 |     await page.route('**/api/v1/documents*',  (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '{"documents":[]}' }))
  47 |     await page.route('**/api/v1/saved-searches/smart-folders', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  48 |     await page.route('**/api/v1/tasks*',      (r) => r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }))
  49 |   })
  50 | 
  51 |   test('html flips to rtl + lang=ar + sidebar lands on the right', async ({ page }) => {
  52 |     // The detector reads cookies first; pre-set so we don't depend
  53 |     // on the UI to drive the language change (that's the selector
  54 |     // spec's job, not this one's).
  55 |     await page.context().addCookies([{
  56 |       name:  'dms_locale',
  57 |       value: 'ar',
  58 |       url:   'http://localhost:4173',
  59 |     }])
  60 | 
  61 |     await page.goto('/')
  62 | 
  63 |     // Wait for the initial paint to commit. The i18n init in
  64 |     // main.tsx awaits before createRoot, so dir is set by the time
  65 |     // any DOM is visible.
  66 |     const html = page.locator('html')
  67 |     await expect(html).toHaveAttribute('dir', 'rtl')
  68 |     await expect(html).toHaveAttribute('lang', 'ar')
  69 | 
  70 |     // Geometry check — the sidebar should be in the right half of
  71 |     // the viewport when the document is RTL. We grab the topbar's
  72 |     // user-avatar button (the right-most chip in LTR) and assert
  73 |     // its bounding box is now in the LEFT half. This catches any
  74 |     // residual hardcoded `left-*`/`right-*` positioning that
  75 |     // survived the mechanical migration.
  76 |     const avatar = page.getByLabel(/Account menu/i)
  77 |     const box    = await avatar.boundingBox()
  78 |     const vw     = page.viewportSize()?.width ?? 1280
  79 |     expect(box).not.toBeNull()
  80 |     expect(box!.x + box!.width / 2).toBeLessThan(vw / 2)
  81 | 
  82 |     // Snapshot for visual diff. Playwright produces a baseline on
  83 |     // first run; subsequent runs diff against it. CI lockfile lives
  84 |     // next to this spec at e2e/__screenshots__.
> 85 |     await expect(page).toHaveScreenshot('rtl-dashboard.png', {
     |     ^ Error: A snapshot doesn't exist at C:\Users\dell\Documents\DOCMS\web\e2e\i18n-rtl-shell.spec.ts-snapshots\rtl-dashboard-chromium-win32.png, writing actual.
  86 |       maxDiffPixels: 5000,                 // tolerate sub-pixel AA
  87 |       fullPage:      true,
  88 |     })
  89 |   })
  90 | 
  91 |   test('ltr is the fallback when no cookie is set', async ({ page }) => {
  92 |     // No cookie, no ?lng — detector falls through to navigator,
  93 |     // which Playwright defaults to en-US.
  94 |     await page.goto('/')
  95 |     await expect(page.locator('html')).toHaveAttribute('dir', 'ltr')
  96 |     await expect(page.locator('html')).toHaveAttribute('lang', /^en/)
  97 |   })
  98 | })
  99 | 
```