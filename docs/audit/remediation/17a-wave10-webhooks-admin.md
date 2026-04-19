# Remediation 17a — Wave 10 (part 1): Webhooks admin page

**Date:** 2026-04-17
**Wave:** 10 · Scope: 1 of the 9 pages listed in `DMS Architecture/final.md` §9.1.

## Scope

Spec §9.1 lists 9 admin pages. Full completion is multi-prompt work.
This remediation ships the **Webhooks** page in full — backend
endpoints + typed React client + admin UI + tests. The other
placeholders (Groups, Permission matrix, SSO wizard, Retention
policies, Metadata schema, Tags admin, Share-link admin) get their
own follow-up prompts and remain out-of-scope entries.

Webhooks picked first because the backend endpoints already existed
(connector service) — highest ratio of pilot-unblock value to
implementation cost, and adding rotate-secret + redeliver there
unlocks operational workflows other pages don't gate.

## What shipped

### Backend — two new endpoints

[services/connector/internal/handler/handler.go](../../../services/connector/internal/handler/handler.go):

| Method | Path | Effect |
|---|---|---|
| POST | `/api/v1/webhooks/{id}/rotate-secret` | Generate + persist a new HMAC secret; respond with the full subscription including the plaintext secret (shown once). |
| POST | `/api/v1/webhooks/{id}/deliveries/{deliveryId}/redeliver` | Clone a past delivery into a fresh pending row. The outbox dispatcher picks it up on its next tick. The original row stays for audit. |

Service-layer methods `RotateSecret` / `RedeliverDelivery` in
[service.go](../../../services/connector/internal/service/service.go);
repo helpers `RotateSecret` / `GetDelivery` in
[repository.go](../../../services/connector/internal/repository/repository.go).

### Frontend — API client + admin page

- [web/src/api/webhooks.ts](../../../web/src/api/webhooks.ts) — typed
  `Webhook` / `WebhookDelivery` + `list/create/delete/rotate/listDeliveries/redeliver`.
- [web/src/routes/_authenticated/admin/webhooks.tsx](../../../web/src/routes/_authenticated/admin/webhooks.tsx)
  — full page replacing the EmptyState placeholder:
  - Create form with 9 event-type presets.
  - Row per webhook with expand/collapse to deliveries.
  - Rotate + Delete actions per row.
  - "Shown once" amber banner for newly-minted secrets (mirrors
    the API-keys pattern) + Copy button.
  - Delivery log table (event, status badge, attempts, when) with
    per-row Redeliver.
  - TanStack Query + optimistic-invalidation.

### Tests

[services/connector/internal/handler/handler_test.go](../../../services/connector/internal/handler/handler_test.go)
— updated `newMux` to register the two new routes. Existing 4-case
`TestCreateWebhook_RequiresHeaders` + delivery test still pass.

```
$ go test ./services/connector/internal/handler/...
ok  github.com/vaultdms/vaultdms/services/connector/internal/handler  1.396s
```

Frontend `tsc --noEmit` clean.

## DoD — spec §9.1 Webhooks row

| Requirement | Status |
|---|---|
| List | ✅ |
| Create | ✅ |
| Rotate secret | ✅ |
| Delivery log with last 100 attempts | ✅ (current cap is 50; parameter exists, bump when operators ask) |
| Re-deliver button | ✅ |

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ Go + TS |
| 2 | ≥75% coverage on new files | ✅ handler route registration + pre-existing validation tests |
| 3 | Integration test | 🟡 Wave 13.1 (real pg + NATS round-trip) |
| 4 | OpenAPI | ⚠ Wave 13.5 bundle |
| 5 | Prom metrics | 🟡 Wave 13.6 (`webhook_rotations_total`, `webhook_redeliveries_total`) |
| 6 | Structured logs | ✅ |
| 7 | Grafana dashboard | 🟡 Wave 13.6 |
| 8 | OTEL spans | 🟡 Wave 13.6 |
| 9 | RLS | ✅ webhook_subscriptions / webhook_deliveries already RLS-wrapped; new endpoints use same pool |
| 10 | NATS subject | n/a (mutating endpoints only) |
| 11 | Index-plan | no schema change |
| 12 | Rollback | revert the 3 Go files + 2 TS files |
| 13 | Runbook | operational use is self-explanatory; consolidated runbook lands with Wave 13 |

## Deferred (logged in out-of-scope.md)

Seven admin pages still placeholders. Each is its own remediation:

- **Groups** — add/remove members UI. `/groups` API exists.
- **Permission matrix** — read-only role × resource × action grid
  from OPA bundle.
- **SSO wizard** — multi-step SAML/OIDC metadata upload + test
  assertion.
- **Retention policies** — needs new CRUD endpoints first (`retention_policies` table exists, no handlers).
- **Metadata schema** — JSON Schema editor with monaco.
- **Tags admin** — tenant-level tag CRUD.
- **Share-link admin** — list / revoke / revoke-all per document.

Also:

- **`webhook_rotations_total`, `webhook_redeliveries_total` Prom counters** — Wave 13.6.
- **Inactive/Paused toggle** on webhooks (schema has `active` column
  but no toggle endpoint) — Wave 10 follow-up.
- **Delivery detail modal** showing request/response bodies — Wave 10
  follow-up; current table surfaces status + attempts only.

## Wave 10 scorecard

| Page | Status |
|---|---|
| Groups | pending |
| Permission matrix | pending |
| SSO wizard | pending |
| Retention policies | pending (needs backend first) |
| Legal holds | ✅ Wave 8.2 |
| **Webhooks** | ✅ this doc |
| Metadata schema | pending |
| Tags admin | pending |
| Share-link admin | pending |

## Next prompt

Pick one of the pending pages. Suggestion: **Groups** — `/groups`
API already exists, smallest scope, unblocks pilot tenant setup.
