import { test, expect, Page } from '@playwright/test'
import { execSync } from 'node:child_process'

// Resilience: prove the platform tolerates the common failure modes
// (service restart, DB hiccup). These run only when the Docker Compose
// stack is present — CI exposes a `DOCKER=1` env flag; local devs set
// it when they have the full stack up. Skipped otherwise so PR CI
// doesn't fail on dev boxes without Docker.

const skipUnlessDocker = () => {
  if (!process.env.E2E_WITH_DOCKER) {
    test.skip(true, 'set E2E_WITH_DOCKER=1 + run docker compose to exercise resilience')
  }
}

async function composeRestart(service: string) {
  // Best-effort: tolerate both `docker compose` (plugin) and
  // `docker-compose` (legacy) — pick whichever is on PATH.
  try {
    execSync(`docker compose restart ${service}`, { stdio: 'pipe' })
  } catch {
    execSync(`docker-compose restart ${service}`, { stdio: 'pipe' })
  }
}

async function login(page: Page) {
  await page.goto('/login')
  await page.getByRole('textbox', { name: 'Tenant' }).fill('acme')
  await page.getByRole('textbox', { name: 'Email' }).fill('admin@acme.local')
  await page.getByRole('textbox', { name: 'Password' }).fill('ChangeMe!Now2026')
  await page.getByRole('button', { name: 'Sign In' }).click()
  await page.waitForURL('**/')
}

test.describe('resilience', () => {
  test('search service restart: indexer catches up', async ({ page, request }) => {
    skipUnlessDocker()
    await login(page)

    // Baseline: issue one search to warm the cache + confirm the service
    // is reachable at all.
    const pre = await request.post('/api/v1/search', { data: { query: 'CONFIDENTIAL' } })
    expect(pre.ok()).toBeTruthy()

    await composeRestart('search')

    // Poll: search service takes up to 30s to reconnect NATS consumers
    // and replay unacked messages. We accept any 2xx or a 503 that
    // resolves within the window.
    const deadline = Date.now() + 60_000
    let ok = false
    while (Date.now() < deadline) {
      const r = await request.post('/api/v1/search', { data: { query: 'CONFIDENTIAL' } })
      if (r.ok()) {
        ok = true
        break
      }
      await new Promise((res) => setTimeout(res, 2000))
    }
    expect(ok, 'search service did not recover within 60s of restart').toBeTruthy()
  })

  test('intelligence service restart: queued OCR resumes', async ({ page, request }) => {
    skipUnlessDocker()
    await login(page)
    // Restart; the Celery worker reconnects to its Redis broker and
    // resumes unacked tasks. We don't have a public queue-depth API,
    // so we treat the /healthz coming back as the recovery signal.
    await composeRestart('intelligence')
    const deadline = Date.now() + 60_000
    let ok = false
    while (Date.now() < deadline) {
      const r = await request.get('/api/v1/intelligence/healthz').catch(() => null)
      if (r && r.ok()) {
        ok = true
        break
      }
      await new Promise((res) => setTimeout(res, 2000))
    }
    expect(ok, 'intelligence service did not recover within 60s').toBeTruthy()
  })

  test('postgres restart: services reconnect without data loss', async ({ page, request }) => {
    skipUnlessDocker()
    await login(page)

    // Capture a pre-restart fact: user count.
    const preUsers = await request.get('/api/v1/admin/users')
    const preBody = (await preUsers.json()) as { users: unknown[] }
    const preCount = preBody.users?.length ?? 0

    await composeRestart('postgres')

    const deadline = Date.now() + 90_000
    let recoveredCount = -1
    while (Date.now() < deadline) {
      const r = await request.get('/api/v1/admin/users').catch(() => null)
      if (r && r.ok()) {
        const body = (await r.json()) as { users: unknown[] }
        recoveredCount = body.users?.length ?? 0
        break
      }
      await new Promise((res) => setTimeout(res, 3000))
    }
    expect(recoveredCount).toBe(preCount)
  })
})
