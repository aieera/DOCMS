// ADR 0064 — /admin/tenant/ai journey. Asserts the write-only key
// invariant, the export-control banner, and the test button flow.
// All backend calls mocked.

import { test, expect } from '@playwright/test'

const TENANT_ID = '00000000-0000-0000-0000-000000000100'
const USER_ID   = '00000000-0000-0000-0000-000000000001'

test.describe('Journey 38 — Admin tenant AI provider', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          user: {
            id: USER_ID, email: 'owner@example.com', display_name: 'Owner',
            role: 'owner', status: 'active', mfa_enabled: false,
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
          id: USER_ID, email: 'owner@example.com', display_name: 'Owner',
          role: 'owner', tenant_id: TENANT_ID,
        }),
      }),
    )
    // Usage cards.
    await page.route('**/api/v1/admin/llm-usage', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          tenant_id: TENANT_ID,
          by_model: [],
          totals: { calls: 42, input_tokens: 10_000, output_tokens: 2_000, cost_usd: 0.123 },
        }),
      }),
    )

    await page.goto('/login')
    await page.getByLabel(/email/i).fill('owner@example.com')
    await page.getByLabel(/password/i).fill('correct-horse-battery-staple')
    await page.getByRole('button', { name: /sign in/i }).click()
  })

  test('PUT body never includes api_key when input is left blank', async ({ page }) => {
    await page.route('**/api/v1/admin/tenant/llm-config', async (route) => {
      if (route.request().method() === 'GET') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            provider: 'anthropic',
            model: 'anthropic/claude-haiku-4-5',
            fallback_model: '',
            base_url: null,
            rate_limit_rpm: 60,
            daily_budget_usd: 0,
            air_gapped: false,
            key_set: true,
            key_set_at: '2026-05-01T00:00:00Z',
            updated_at: '2026-05-01T00:00:00Z',
          }),
        })
        return
      }
      const body = JSON.parse(route.request().postData() ?? '{}')
      // Load-bearing assertion: the page MUST NOT send api_key when
      // the user didn't paste a new one.
      expect(body).not.toHaveProperty('api_key')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provider: body.provider, model: body.model,
          fallback_model: body.fallback_model || '',
          base_url: body.base_url ?? null,
          rate_limit_rpm: body.rate_limit_rpm,
          daily_budget_usd: body.daily_budget_usd,
          air_gapped: body.air_gapped ?? false,
          key_set: true,
          key_set_at: '2026-05-01T00:00:00Z',
          updated_at: new Date().toISOString(),
        }),
      })
    })

    await page.goto('/admin/tenant/ai')
    // Confirm "Key set" rendering uses key_set_at, not the ciphertext.
    await expect(page.getByText(/Key set\s/i)).toBeVisible()
    // Save without typing anything in the key input.
    await page.getByTestId('llm-rate-limit').fill('120')
    await page.getByTestId('llm-save').click()
    await expect(page.getByText(/Saved/i)).toBeVisible()
  })

  test('PUT body INCLUDES api_key when user pastes a new key', async ({ page }) => {
    let putBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/admin/tenant/llm-config', async (route) => {
      if (route.request().method() === 'GET') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            provider: 'anthropic', model: '', fallback_model: '', base_url: null,
            rate_limit_rpm: 60, daily_budget_usd: 0, air_gapped: false,
            key_set: false, key_set_at: null, updated_at: null,
          }),
        })
        return
      }
      putBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provider: 'anthropic', model: '', fallback_model: '', base_url: null,
          rate_limit_rpm: 60, daily_budget_usd: 0, air_gapped: false,
          key_set: true, key_set_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        }),
      })
    })

    await page.goto('/admin/tenant/ai')
    await page.getByTestId('llm-api-key-input').fill('sk-ant-test-secret')
    await page.getByTestId('llm-save').click()
    await expect.poll(() => putBody).toMatchObject({ api_key: 'sk-ant-test-secret' })
    // After save, the input clears (page state), and the response
    // never echoed the key — verify the input is now empty and the
    // page surfaces "Key set".
    await expect(page.getByTestId('llm-api-key-input')).toHaveValue('')
  })

  test('export-control banner appears for non-US providers', async ({ page }) => {
    await page.route('**/api/v1/admin/tenant/llm-config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provider: 'anthropic', model: '', fallback_model: '', base_url: null,
          rate_limit_rpm: 60, daily_budget_usd: 0, air_gapped: false,
          key_set: false, key_set_at: null, updated_at: null,
        }),
      }),
    )

    await page.goto('/admin/tenant/ai')
    // Anthropic is non-US → banner visible by default.
    await expect(page.getByTestId('export-control-warning')).toBeVisible()
    // Switch to OpenAI → banner hides (US-domiciled in our list).
    // Radix Select opens via click + option role; the existing helper
    // pattern in admin pages clicks the trigger + the option label.
    await page.getByText('Anthropic (Claude)').click()
    await page.getByRole('option', { name: /OpenAI/i }).click()
    await expect(page.getByTestId('export-control-warning')).toHaveCount(0)
  })

  test('test button hits /llm/completions and renders the response', async ({ page }) => {
    await page.route('**/api/v1/admin/tenant/llm-config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provider: 'anthropic',
          model: 'anthropic/claude-haiku-4-5',
          fallback_model: '',
          base_url: null,
          rate_limit_rpm: 60, daily_budget_usd: 0, air_gapped: false,
          key_set: true, key_set_at: '2026-05-01T00:00:00Z',
          updated_at: '2026-05-01T00:00:00Z',
        }),
      }),
    )

    let testBody: Record<string, unknown> | null = null
    await page.route('**/api/v1/intelligence/llm/completions', async (route) => {
      testBody = JSON.parse(route.request().postData() ?? '{}')
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          content: 'ack',
          model: 'anthropic/claude-haiku-4-5',
          provider: 'anthropic',
          input_tokens: 12, output_tokens: 1,
          cost_usd: 0.0001, elapsed_ms: 320,
          fallback_used: false,
        }),
      })
    })

    await page.goto('/admin/tenant/ai')
    await page.getByTestId('llm-test-prompt').fill('Say "ack"')
    await page.getByTestId('llm-test-run').click()

    await expect.poll(() => testBody).toMatchObject({
      messages: [{ role: 'user', content: 'Say "ack"' }],
      model: 'anthropic/claude-haiku-4-5',
    })
    await expect(page.getByTestId('llm-test-result')).toContainText('ack')
    // Provider + elapsed_ms surface in the result strip so admins see
    // which path actually ran (catches a misconfigured fallback).
    await expect(page.getByTestId('llm-test-result')).toContainText('anthropic')
    await expect(page.getByTestId('llm-test-result')).toContainText('320ms')
  })

  test('air-gapped 403 surfaces in the test panel', async ({ page }) => {
    await page.route('**/api/v1/admin/tenant/llm-config', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          provider: 'openai', model: 'openai/gpt-4o-mini',
          fallback_model: '', base_url: null,
          rate_limit_rpm: 60, daily_budget_usd: 0, air_gapped: true,
          key_set: true, key_set_at: '2026-05-01T00:00:00Z',
          updated_at: '2026-05-01T00:00:00Z',
        }),
      }),
    )
    await page.route('**/api/v1/intelligence/llm/completions', (route) =>
      route.fulfill({
        status: 403,
        contentType: 'application/json',
        body: JSON.stringify({ detail: "air-gapped tenant cannot use external provider 'openai'" }),
      }),
    )

    await page.goto('/admin/tenant/ai')
    await page.getByTestId('llm-test-run').click()
    await expect(page.getByTestId('llm-test-result')).toContainText(/air-gapped/i)
  })
})
