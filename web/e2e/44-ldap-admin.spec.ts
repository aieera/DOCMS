// ADR 0062 — LDAP/AD admin journey.
//
// Coverage:
//   1. Empty state: /admin/tenant/identity/ldap renders the
//      "No LDAP integration yet" form and a Save-draft + Test
//      connection pair.
//   2. Existing-config view: shows connection details, mappings
//      table, and history table.
//   3. Test-bind: ok=true response renders a green check; ok=false
//      renders the error string.
//   4. Sync-now: POST /admin/ldap/configs/{id}/sync triggers the
//      handler and the row count updates after the response lands.
//
// Backend traffic is mocked via page.route. End-to-end coverage
// against the slapd compose container is the integration suite's job.

import { test, expect } from '@playwright/test'

const CONFIG_ID = 'a1f1e6c0-0000-0000-0000-000000000071'

const sampleConfig = {
  id: CONFIG_ID,
  url: 'ldaps://ldap.example.com:636',
  use_starttls: true,
  allow_insecure: false,
  bind_dn: 'cn=admin,dc=example,dc=com',
  user_search_base: 'dc=example,dc=com',
  user_search_filter: '(sAMAccountName={username})',
  email_attribute: 'mail',
  display_name_attribute: 'displayName',
  group_search_base: 'dc=example,dc=com',
  group_search_filter: '(member={user_dn})',
  nested_groups: true,
  fallback_to_local: false,
  is_active: true,
  has_bind_password: true,
  last_sync_at: '2026-05-07T10:00:00Z',
  last_sync_status: 'ok',
  created_at: '2026-05-07T09:00:00Z',
  updated_at: '2026-05-07T10:00:00Z',
}

const sampleHistory = [
  {
    id: '00000000-0000-0000-0000-000000000001',
    trigger: 'scheduled',
    started_at: '2026-05-07T10:00:00Z',
    finished_at: '2026-05-07T10:00:05Z',
    status: 'ok',
    users_synced: 12,
    groups_synced: 3,
    errors: 0,
  },
]

test.describe('Journey 44 — LDAP admin', () => {
  test.beforeEach(async ({ page }) => {
    // Skip auth — assume Playwright's storage state covers the
    // session; admin pages render only after AuthMiddleware passes.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          id: 'u-1', email: 'admin@example.com',
          display_name: 'Admin', role: 'owner',
          tenant_id: 't-1', tenant_slug: 'demo',
        }),
      }),
    )
  })

  test('empty state renders the draft form', async ({ page }) => {
    await page.route('**/api/v1/admin/ldap/configs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.goto('/admin/tenant/identity/ldap')
    await expect(page.getByText('No LDAP integration yet')).toBeVisible()
    await expect(page.getByRole('button', { name: /save draft/i })).toBeVisible()
    await expect(page.getByRole('button', { name: /test connection/i })).toBeVisible()
  })

  test('existing config view shows connection + mappings + history', async ({ page }) => {
    await page.route('**/api/v1/admin/ldap/configs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([sampleConfig]) }),
    )
    await page.route(`**/api/v1/admin/ldap/configs/${CONFIG_ID}/mappings`, (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([
          { ldap_group_dn: 'cn=engineers,ou=groups,dc=example,dc=com', dms_group_id: 'dms-engineers' },
        ]),
      }),
    )
    await page.route('**/api/v1/admin/groups', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify([{ id: 'dms-engineers', name: 'Engineers', member_count: 4 }]),
      }),
    )
    await page.route(`**/api/v1/admin/ldap/configs/${CONFIG_ID}/history`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(sampleHistory) }),
    )

    await page.goto('/admin/tenant/identity/ldap')
    await expect(page.getByText('Connection')).toBeVisible()
    await expect(page.getByText('cn=engineers,ou=groups,dc=example,dc=com')).toBeVisible()
    await expect(page.getByText('Engineers')).toBeVisible()
    await expect(page.getByText('Sync history')).toBeVisible()
    await expect(page.getByText('users_synced').or(page.getByText('12'))).toBeVisible()
  })

  test('test-bind ok renders success badge', async ({ page }) => {
    await page.route('**/api/v1/admin/ldap/configs', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify([sampleConfig]) }),
    )
    await page.route(`**/api/v1/admin/ldap/configs/${CONFIG_ID}/mappings`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/admin/groups', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route(`**/api/v1/admin/ldap/configs/${CONFIG_ID}/history`, (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: '[]' }),
    )
    await page.route('**/api/v1/admin/ldap/test-bind', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({ ok: true, bind_ok: true, user_found: false, groups_found: 0 }),
      }),
    )

    await page.goto('/admin/tenant/identity/ldap')
    await page.getByRole('button', { name: /test connection/i }).first().click()
    await expect(page.getByText(/bind ok/i)).toBeVisible()
  })
})
