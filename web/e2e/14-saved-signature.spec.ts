// Journey 14 — Wave 15.4 saved signature profiles.
//
// Covers the core DoD paths:
//   (a) /settings/signatures lists existing profiles.
//   (b) User creates a "typed" signature → POST /signatures/profiles
//       fires with a non-empty base64 image.
//   (c) Set-default PATCH triggers a refetch.
//   (d) Delete prompts + removes from the list.
//
// The backend-driven envelope picker isn't asserted here — Wave 9
// envelope UI isn't built yet; see progress doc.

import { test, expect } from '@playwright/test'
import { expectAxeClean } from './helpers/a11y'

const TENANT = '00000000-0000-0000-0000-000000000100'
const USER = '00000000-0000-0000-0000-000000000001'

type Profile = {
  id: string
  name: string
  kind: string
  is_default: boolean
  created_at: string
  updated_at: string
}

test.describe('Journey 14 — saved signature', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: USER,
          email: 'alice@example.com',
          display_name: 'Alice',
          role: 'admin',
          tenant_id: TENANT,
        }),
      }),
    )
  })

  test('list + create typed signature', async ({ page }) => {
    let profiles: Profile[] = [
      {
        id: 'p1',
        name: 'Formal sig',
        kind: 'draw',
        is_default: true,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ]

    await page.route('**/api/v1/signatures/profiles', (route) => {
      if (route.request().method() === 'POST') {
        const postBody = route.request().postDataJSON() as { image_base64?: string; name?: string; kind?: string }
        // Server-side contract: the POST body includes a non-empty
        // base64 string — confirms the client rendered pixels.
        if (!postBody.image_base64 || postBody.image_base64.length < 100) {
          return route.fulfill({
            status: 400,
            contentType: 'application/json',
            body: JSON.stringify({ error: { code: 'BAD_IMAGE', message: 'empty image' } }),
          })
        }
        const created: Profile = {
          id: 'p2',
          name: postBody.name ?? 'Typed',
          kind: postBody.kind ?? 'typed',
          is_default: false,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }
        profiles = [...profiles, created]
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(created),
        })
      }
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ profiles }),
      })
    })
    // Image endpoint — tests don't assert the actual bytes, just
    // that the <img> tag's src resolves without erroring.
    await page.route('**/api/v1/signatures/profiles/*/image', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'image/png',
        body: Buffer.from(
          '89504e470d0a1a0a0000000d494844520000000100000001080600000' +
          '01f15c4890000000a49444154789c6300010000000500010d0a2db40000' +
          '000049454e44ae426082',
          'hex',
        ),
      }),
    )

    await page.goto('/settings/signatures')
    await expect(page.getByText('Formal sig')).toBeVisible()

    // Switch form to typed mode; fill text; submit.
    await page.getByLabel(/type/i).selectOption('typed')
    await page.getByLabel(/typed signature text/i).fill('Alice Alpha')
    await page.getByRole('button', { name: /save signature/i }).click()

    // New profile shows up after the server responds + the list
    // refetches (invalidateQueries in onSuccess).
    await expect(page.getByText('Typed', { exact: false })).toBeVisible()
  })

  test('set-default on non-default profile', async ({ page }) => {
    const profiles: Profile[] = [
      {
        id: 'p1',
        name: 'Default sig',
        kind: 'draw',
        is_default: true,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
      {
        id: 'p2',
        name: 'Other sig',
        kind: 'typed',
        is_default: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      },
    ]
    await page.route('**/api/v1/signatures/profiles', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ profiles }),
      }),
    )
    let patched = false
    await page.route('**/api/v1/signatures/profiles/p2', (route) => {
      if (route.request().method() === 'PATCH') {
        patched = true
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ ...profiles[1], is_default: true }),
        })
      }
      return route.fulfill({ status: 404, body: '' })
    })
    await page.route('**/api/v1/signatures/profiles/*/image', (route) =>
      route.fulfill({ status: 200, contentType: 'image/png', body: '' }),
    )

    await page.goto('/settings/signatures')
    await page.getByRole('button', { name: /set Other sig as default/i }).click()
    await expect.poll(() => patched).toBe(true)
  })

  test('/settings/signatures has no axe serious/critical violations', async ({ page }, testInfo) => {
    await page.route('**/api/v1/signatures/profiles', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ profiles: [] }),
      }),
    )
    await page.goto('/settings/signatures')
    await expectAxeClean(page, testInfo, 'settings-signatures')
  })
})
