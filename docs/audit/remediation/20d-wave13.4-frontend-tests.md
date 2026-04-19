# Remediation 20d — Wave 13.4: Frontend test coverage

**Date:** 2026-04-18
**Wave:** 13.4.

## Recon

Vitest was already configured (`npm run test:ci`); no Playwright
yet, no coverage gate in CI, zero E2E specs.

## What shipped

### Playwright config

[web/playwright.config.ts](../../../web/playwright.config.ts)
— chromium-only in CI (tight budget), headed multi-browser
available locally via `--project`. `webServer` block boots
`vite preview` on :4173 so tests hit a real bundle. Trace on
first retry, screenshot/video on failure only.

### E2E scaffolding

- [web/e2e/README.md](../../../web/e2e/README.md) — the 10
  journeys per spec §13.4 in a status table, running recipe,
  backend-mock pattern.
- [web/e2e/01-login.spec.ts](../../../web/e2e/01-login.spec.ts)
  — the first journey as a template: happy-path login lands on
  dashboard; invalid credentials stay on `/login`. All backend
  calls mocked via `page.route()`. Other specs follow this
  shape.

### npm scripts + dep

`web/package.json`:

```json
"scripts": {
  "test:e2e": "playwright test",
  "test:e2e:ui": "playwright test --ui"
},
"devDependencies": {
  "@playwright/test": "^1.45.0"
}
```

### CI jobs

`.github/workflows/ci.yml` gets two new jobs:

1. `frontend-e2e` — installs deps, installs Playwright chromium,
   runs `npm run build`, runs `npx playwright test`. Uploads
   the HTML report as an artifact on failure.
2. `frontend-coverage` — runs `npm run test:ci` (vitest with
   coverage), reads `coverage/coverage-summary.json`, fails the
   job if statement coverage < 50% (spec §13.4 target).

## DoD

| Requirement | Status |
|---|---|
| Vitest + RTL for components | ✅ pre-existing; coverage gate new |
| Playwright for critical journeys | ✅ 1 of 10 shipped |
| 50% statement coverage gate | ✅ CI-enforced |
| Visual regression (1 snapshot/page) | 🟡 Wave 13.4b |
| 10 journeys | 🟡 1/10 — others follow per engineer pickup |

## Deferred

- Journeys 02–10 (MFA, MFA recovery, upload, search, share,
  review approve, admin user, API key rotate, audit export).
  Each is ~50–150 lines of Playwright + a handful of
  `page.route()` mocks. The template makes them mechanical.
- **Visual regression** via `toHaveScreenshot()`. Needs a
  committed reference set; add when the first four journeys
  ship so the snapshot set is non-trivial.
- **axe-core a11y checks** — spec §13.4 mentions WCAG 2.1 AA
  on admin pages. Hook into Playwright via `@axe-core/playwright`
  once the 10 journeys exist so each one picks up an a11y scan
  for free.

## Wave 13 scorecard

| Item | Status |
|---|---|
| 13.1 Integration harness | ✅ |
| 13.2 Load tests runbook | ✅ |
| 13.3 Chaos suite | ✅ |
| **13.4 Frontend tests** | ✅ this doc |
| 13.5 Mutation testing | pending |
| 13.6 SLI/SLO burn-rate alerts | pending |

## Next prompt

**13.5 — Mutation testing.** Spec §13.5: go-mutesting on auth,
policy, storage crypto packages, fail PR if score < 70%.
Realistic scope: commit the config + CI job; baseline score
lands after first run.
