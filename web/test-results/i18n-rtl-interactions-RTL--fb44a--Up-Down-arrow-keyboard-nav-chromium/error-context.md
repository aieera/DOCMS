# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: i18n-rtl-interactions.spec.ts >> RTL interactions — ADR 0108 >> user-menu dropdown opens with Up/Down arrow keyboard nav
- Location: e2e\i18n-rtl-interactions.spec.ts:75:3

# Error details

```
Error: expect(locator).toBeFocused() failed

Locator:  getByRole('menuitem').first()
Expected: focused
Received: inactive
Timeout:  10000ms

Call log:
  - Expect "toBeFocused" with timeout 10000ms
  - waiting for getByRole('menuitem').first()
    13 × locator resolved to <div tabindex="-1" role="menuitem" data-orientation="vertical" data-radix-collection-item="" class="relative flex cursor-default select-none items-center gap-2 rounded-sm px-2 py-1.5 text-sm outline-none transition-colors focus:bg-accent focus:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0">…</div>
       - unexpected value "inactive"

```

# Page snapshot

```yaml
- generic:
  - generic:
    - generic:
      - complementary:
        - generic:
          - generic:
            - link:
              - /url: /
              - generic:
                - img
              - generic: VaultDMS
            - button:
              - img
          - generic:
            - button:
              - generic: A
              - generic:
                - generic: Alice
                - generic: alice@example.com
              - img
          - navigation:
            - generic:
              - generic: مساحة العمل
              - link:
                - /url: /
                - img
                - generic: لوحة التحكم
              - link:
                - /url: /workspaces
                - img
                - generic: مساحات العمل
              - link:
                - /url: /search
                - img
                - generic: بحث
              - link:
                - /url: /ask
                - img
                - generic: اسأل
              - link:
                - /url: /saved-searches
                - img
                - generic: عمليات البحث المحفوظة
              - link:
                - /url: /clauses
                - img
                - generic: البنود
            - generic:
              - generic: صندوق الوارد
              - link:
                - /url: /tasks
                - img
                - generic: المهام
              - link:
                - /url: /notifications
                - img
                - generic: الإشعارات
              - link:
                - /url: /trash
                - img
                - generic: المهملات
            - generic:
              - generic: الإعدادات
              - link:
                - /url: /settings/security
                - img
                - generic: إعداداتي
              - link:
                - /url: /admin
                - img
                - generic: الإدارة
      - generic:
        - banner:
          - generic:
            - navigation:
              - link:
                - /url: /
                - img
                - generic: Home
          - generic:
            - button:
              - img
              - generic: ابحث في المستندات…
              - generic:
                - generic: ⌘
                - text: K
            - button:
              - img
            - button:
              - img
            - button:
              - img
            - combobox:
              - img
              - generic: العربية
              - img
            - button [expanded]: A
        - main:
          - generic:
            - generic:
              - generic:
                - generic:
                  - heading [level=1]: Good evening, Alice
                  - generic:
                    - text: Workspace overview ·
                    - code: "00000000"
              - generic:
                - link:
                  - /url: /workspaces
                  - generic:
                    - generic:
                      - generic:
                        - img
                      - img
                    - paragraph: Workspaces
                    - generic: "0"
                    - paragraph: tenant total
                - link:
                  - /url: /tasks
                  - generic:
                    - generic:
                      - generic:
                        - img
                      - img
                    - paragraph: Open tasks
                    - generic: "0"
                    - paragraph: assigned to you
                - link:
                  - /url: /notifications
                  - generic:
                    - generic:
                      - generic:
                        - img
                      - img
                    - paragraph: Unread notifications
                    - generic: "0"
                    - paragraph: across all channels
              - region:
                - heading [level=2]: Quick actions
                - generic:
                  - link:
                    - /url: /search
                    - generic:
                      - img
                    - generic:
                      - generic:
                        - paragraph: Search documents
                        - generic: ⌘K
                      - paragraph: Full-text + semantic across the tenant
                  - link:
                    - /url: /ask
                    - generic:
                      - img
                    - generic:
                      - generic:
                        - paragraph: Ask the corpus
                      - paragraph: RAG over the documents you can see
                  - link:
                    - /url: /workspaces
                    - generic:
                      - img
                    - generic:
                      - generic:
                        - paragraph: Upload
                      - paragraph: Drag a file into a workspace
                  - link:
                    - /url: /workflows/designer
                    - generic:
                      - img
                    - generic:
                      - generic:
                        - paragraph: Design a workflow
                      - paragraph: Approval chains + signature steps
              - generic:
                - generic:
                  - generic:
                    - generic:
                      - heading [level=2]: My open tasks
                      - paragraph: Approval steps + to-dos assigned to you.
                    - button:
                      - text: View all
                      - img
                  - generic:
                    - generic:
                      - generic:
                        - img
                      - paragraph: Inbox zero
                      - paragraph: No open tasks. New work will land here.
                - generic:
                  - generic:
                    - generic:
                      - heading [level=2]: Recent activity
                      - paragraph: Latest notifications across all channels.
                    - button:
                      - text: View all
                      - img
                  - generic:
                    - generic:
                      - generic:
                        - img
                      - paragraph: No activity yet
                      - paragraph: As work happens in your workspaces it'll show up here.
    - region "Notifications alt+T"
  - menu "Account menu" [ref=e1]:
    - generic [ref=e3]:
      - paragraph [ref=e4]: Alice
      - paragraph [ref=e5]: alice@example.com
    - separator [ref=e6]
    - menuitem "الإعدادات" [ref=e7]:
      - img
      - text: الإعدادات
    - separator [ref=e8]
    - menuitem "تسجيل الخروج" [active] [ref=e9]:
      - img
      - text: تسجيل الخروج
```

# Test source

```ts
  1   | // ADR 0108 — RTL interaction smoke.
  2   | //
  3   | // Verifies behaviour that's hard to catch with static class lints:
  4   | //   1. A modal dialog renders body copy with `text-start`, so a
  5   | //      multi-line block reads from the leading edge of the dialog.
  6   | //   2. A user-menu dropdown opens and the keyboard arrow navigation
  7   | //      works the same way in RTL as in LTR (Up/Down move through
  8   | //      items; Left/Right are not used for menu nav).
  9   | //   3. The DataTable header is start-aligned in both directions.
  10  | //
  11  | // Strategy:
  12  | //   * Pre-seed the `dms_locale` cookie so the i18n init detector
  13  | //     resolves to Arabic before first paint — no UI-driven locale
  14  | //     switch needed.
  15  | //   * Mock /auth/me + the few list endpoints the dashboard touches
  16  | //     so the layout mounts without a real backend.
  17  | //   * Only assert structural things (attributes, bounding boxes,
  18  | //     keyboard focus). Don't assert specific translated strings —
  19  | //     those are Prompt-4's responsibility.
  20  | import { test, expect } from '@playwright/test'
  21  | 
  22  | test.describe('RTL interactions — ADR 0108', () => {
  23  |   test.beforeEach(async ({ page, context }) => {
  24  |     await context.addCookies([{
  25  |       name:  'dms_locale',
  26  |       value: 'ar',
  27  |       url:   'http://localhost:4173',
  28  |     }])
  29  | 
  30  |     await page.route('**/api/v1/auth/me', (r) =>
  31  |       r.fulfill({
  32  |         status: 200,
  33  |         contentType: 'application/json',
  34  |         body: JSON.stringify({
  35  |           id:           '00000000-0000-0000-0000-000000000001',
  36  |           email:        'alice@example.com',
  37  |           display_name: 'Alice',
  38  |           role:         'admin',
  39  |           tenant_id:    '00000000-0000-0000-0000-000000000100',
  40  |           locale:       'ar',
  41  |           mfa_enabled:  false,
  42  |         }),
  43  |       }),
  44  |     )
  45  |     // Empty list responses keep the shell mounted without route-level
  46  |     // surprises.
  47  |     for (const url of [
  48  |       '**/api/v1/workspaces*',
  49  |       '**/api/v1/documents*',
  50  |       '**/api/v1/saved-searches/smart-folders',
  51  |       '**/api/v1/tasks*',
  52  |       '**/api/v1/notifications*',
  53  |     ]) {
  54  |       await page.route(url, (r) =>
  55  |         r.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
  56  |       )
  57  |     }
  58  |   })
  59  | 
  60  |   test('html dir=rtl + sidebar lives on the right edge', async ({ page }) => {
  61  |     await page.goto('/')
  62  |     await expect(page.locator('html')).toHaveAttribute('dir', 'rtl')
  63  | 
  64  |     // Topbar Account avatar — in LTR this is the rightmost chip in
  65  |     // the topbar; under RTL it MUST end up on the left half because
  66  |     // the topbar mirrors. If a leftover hardcoded `left-*`/`right-*`
  67  |     // survived the migration, this assertion catches it.
  68  |     const avatar = page.getByLabel(/Account menu/i)
  69  |     const box    = await avatar.boundingBox()
  70  |     const vw     = page.viewportSize()?.width ?? 1280
  71  |     expect(box).not.toBeNull()
  72  |     expect(box!.x + box!.width / 2).toBeLessThan(vw / 2)
  73  |   })
  74  | 
  75  |   test('user-menu dropdown opens with Up/Down arrow keyboard nav', async ({ page }) => {
  76  |     await page.goto('/')
  77  | 
  78  |     // Open via keyboard so we exercise focus traversal too.
  79  |     const avatar = page.getByLabel(/Account menu/i)
  80  |     await avatar.focus()
  81  |     await page.keyboard.press('Enter')
  82  | 
  83  |     // The menu items render in DOM order: Settings then Log out.
  84  |     // ArrowDown lands on the first item, second ArrowDown on the
  85  |     // second — same behaviour as LTR. Left/Right are NOT used.
  86  |     await page.keyboard.press('ArrowDown')
  87  |     const firstItem = page.getByRole('menuitem').first()
> 88  |     await expect(firstItem).toBeFocused()
      |                             ^ Error: expect(locator).toBeFocused() failed
  89  | 
  90  |     await page.keyboard.press('ArrowDown')
  91  |     const secondItem = page.getByRole('menuitem').nth(1)
  92  |     await expect(secondItem).toBeFocused()
  93  |   })
  94  | 
  95  |   test('modal body copy is text-start (leading edge of the dialog)', async ({ page }) => {
  96  |     // The simplest dialog we can reach without auth or a deep mock
  97  |     // chain: open the language selector itself. The selector is a
  98  |     // Radix Select, which is a dialog-like primitive. We confirm its
  99  |     // content panel renders with start-aligned text.
  100 |     await page.goto('/')
  101 |     const langBtn = page.getByLabel(/Language|اللغة/i)
  102 |     await langBtn.click()
  103 | 
  104 |     // The options panel should be visible; assert one item exists.
  105 |     const arItem = page.getByRole('option', { name: /العربية/ })
  106 |     await expect(arItem).toBeVisible()
  107 | 
  108 |     // Compute computed-style text-align on the option. CSS-logical
  109 |     // `text-start` resolves to `start`. We accept either the keyword
  110 |     // or its physical equivalent under `dir="rtl"` ('right').
  111 |     const align = await arItem.evaluate((el) =>
  112 |       getComputedStyle(el).textAlign,
  113 |     )
  114 |     expect(['start', 'right']).toContain(align)
  115 |   })
  116 | })
  117 | 
```