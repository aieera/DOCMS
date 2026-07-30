# Epic 11 — billing service: fixes

The Epic 11 adversarial review of the billing service (subscriptions,
entitlements/feature-flags, usage metering, Stripe) confirmed 6 findings that
reduce to 3 distinct defects. All 3 are fixed on this branch.

## Fixed
| # | Sev | What |
|---|-----|------|
| 1 / 2 | HIGH | **Self-service paywall bypass.** `PUT /api/v1/admin/settings` (org-**owner** role — a normal tenant role, not a platform admin) decoded a full `model.FeatureFlags` and wrote it verbatim via `UpdateFeatureFlags`, with no reconciliation against the tenant's plan — so a standard-plan owner could set `ai_enabled` / `sso_enabled` / `e_signatures` / `custom_branding` / `advanced_workflow` / `data_rooms` all true and unlock enterprise features **for free**. Split the path: the tenant-facing handler now calls `UpdateTenantFeatureFlags`, which **clamps** requested paid entitlements to the tenant's plan (`ClampEntitlementsTo` — an owner can toggle within/below their entitlements but cannot self-grant above them; the free `AutoDetectDocumentType` preference is preserved; fail-closed to `standard` if the plan can't be resolved). The trusted internal `/internal/v1/.../features` endpoint keeps the unclamped `UpdateFeatureFlags` for ops/provisioning overrides. |
| 3 / 4 | HIGH / MED | **Stripe webhook fail-open.** `webhook.go` only verified the HMAC signature `if webhookSecret != ""` and otherwise processed **unsigned JSON**, and `main.go` never enforced a non-empty secret despite the code's own contract. A prod deploy with `STRIPE_WEBHOOK_SECRET` unset would trust forged `checkout.session.completed` / `invoice.paid` events (mark a subscription active for any tenant). `main.go` now calls `RequireSecret("stripe_webhook_secret", …)` — **prod cannot boot** without it (env-aware, dev unaffected) — and the empty-secret path logs a loud warning that it is processing an UNVERIFIED event (dev-only). |
| 5 | MED | **`requireAPIKey` fail-open.** The internal-endpoint gate (`if apiKey != "" && header != apiKey`) left `provision` + `update-features` **unauthenticated** when the configured key was empty (documented "dev mode", asserted by a test). Closed the real (prod) exposure the same way: `main.go` now calls `RequireSecret("internal_api_key", …)` so **prod cannot boot** with an empty key, while the intentional dev-open behavior (and its tests) is preserved. These endpoints also sit behind `RequireGatewaySignature`. |

Verified: billing build + vet clean; internal test suite passes; gofmt clean.
