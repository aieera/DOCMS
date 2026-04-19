// Wave 13.4 — Playwright config for the 10 critical user journeys
// listed in spec §13.4. The first journey ships here as a
// working template; the other nine follow as frontend engineers
// pick them up.
//
// Conventions this config enforces:
//   - `e2e/*.spec.ts` is the only test location.
//   - One browser (chromium) in CI to keep the budget tight;
//     local dev can opt into the full matrix with --project.
//   - Vite preview server spins up before the run via
//     `webServer`; no global hooks, no fixtures-as-singletons.
//   - Screenshots + videos on failure only. Trace on first retry.

import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 2 : undefined,
  reporter: process.env.CI ? [['github'], ['html', { open: 'never' }]] : 'list',
  timeout: 60_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL: process.env.E2E_BASE_URL ?? 'http://localhost:4173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    actionTimeout: 10_000,
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    // Uncomment locally for cross-browser verification before a release.
    // { name: 'firefox', use: { ...devices['Desktop Firefox'] } },
    // { name: 'webkit',  use: { ...devices['Desktop Safari'] } },
  ],
  webServer: {
    // Assumes `npm run build` already produced a static bundle;
    // vite preview serves it on :4173 with SPA fallback.
    command: 'npm run preview -- --host 127.0.0.1 --port 4173',
    url: 'http://localhost:4173',
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    // The real backends aren't reachable from Playwright; tests
    // that need API responses set up route mocks with
    // page.route(). See e2e/01-login.spec.ts for the pattern.
  },
})
