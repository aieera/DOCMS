# Frontend E2E tests

Wave 13.4. Ten critical user journeys per spec §13.4:

| # | Journey | Spec file | Status |
|---|---|---|---|
| 1 | Login (happy path) | `01-login.spec.ts` | ✅ template shipped |
| 2 | MFA setup + verify | `02-mfa.spec.ts` | pending |
| 3 | MFA recovery code login | `03-mfa-recovery.spec.ts` | pending |
| 4 | Document upload | `04-upload.spec.ts` | pending |
| 5 | Search | `05-search.spec.ts` | pending |
| 6 | Share link create + access | `06-share.spec.ts` | pending |
| 7 | Review approve | `07-review-approve.spec.ts` | pending |
| 8 | Admin user create / invite | `08-admin-user.spec.ts` | pending |
| 9 | API key rotate | `09-api-key.spec.ts` | pending |
| 10 | Audit log export | `10-audit-export.spec.ts` | pending |

## Running

```bash
cd web
npm install
npx playwright install --with-deps
npm run build          # vite preview serves this on :4173
npx playwright test    # headless, one browser
npx playwright test --ui  # interactive
```

CI runs `npx playwright test --project=chromium` with retries=2.

## Backend mocking

Playwright never talks to a real backend — tests use
`page.route()` to intercept API calls and return deterministic
fixtures. The template spec (`01-login.spec.ts`) shows the
pattern.

## Coverage gate

Unit-test coverage (vitest) has its own gate in
`.github/workflows/ci.yml` at 50% statement coverage on
`web/src/**` — the spec §13.4 target for end-of-wave. Playwright
journeys are binary pass/fail, not counted in that percentage.

## Visual regression

Not yet wired. Spec §13.4 calls for one snapshot per page;
Playwright's `toHaveScreenshot()` handles this once a reference
set is committed. Follow-up when the first four journeys ship.
