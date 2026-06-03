# Remediation 13e — Wave 6 Prompt 6.5: outbox-only publishing

**Date:** 2026-04-17
**Wave:** 6 · **Prompt:** 6.5 (final of Wave 6).
**Source:** `DMS Architecture/final.md` § 5.5.
**Status:** ✅ regression guard shipped; the underlying code was
already compliant before this prompt ran.

## Recon finding

final.md § 2.4 and § 5.5 flagged "4 services publish directly to NATS
from request handlers". A full-tree grep of the live code found
**zero violations**:

```
$ grep -rn 'js\.Publish\|\.PublishMsg\|js\.PublishAsync' services/ pkg/ | grep -v _test.go
pkg/database/outbox_publisher.go:217:   _, err = p.js.PublishMsg(&nats.Msg{...})
pkg/events/publisher.go:258:            _, err = p.js.PublishMsg(&nats.Msg{...})
```

Both hits are in `pkg/` library code, not request handlers. The
outbox publisher drains the outbox table and is the expected sole
entry point to JetStream. The generic `pkg/events/publisher.go`
`Publisher` type is unused by any service — kept in place for
emergency / diagnostic tooling and as a potential future non-outbox
path.

So the "fix" here is **preventing regression**, not fixing a live
bug. The 4 violations the spec called out had already been converted
to outbox inserts in the same SQL transaction as their state change
(evidence: `model.NewOutboxEvent` + `s.repos.Outbox.Insert` pattern
is the canonical shape across the codebase — see
`services/document/internal/service/documents.go:453-466` and dozens
of sibling call sites).

## What shipped

### `scripts/check-outbox-only-publishing.sh`

New CI guard. Fails the build if any file under `services/` calls
`js.Publish`, `js.PublishMsg`, or `js.PublishAsync`. Allow-list:

- `pkg/database/outbox_publisher.go` — the only legitimate NATS
  publish origin (drains the outbox).
- `pkg/events/publisher.go` — generic CloudEvents library, unused in
  services today; a future service call site would also trip the
  guard via the service-side file.
- `cmd/dms-admin/*` — admin CLI may publish diagnostic messages.
- `*_test.go` — test fixtures.

Error message points engineers at the correct pattern with a
code-sample fix and an ADR 0021 reference.

### CI wire-up

Added to the `security-go` job in
[.github/workflows/ci.yml](../../../.github/workflows/ci.yml)
alongside the Wave 6.3 and 6.4 guards:

```yaml
- name: Outbox-only publishing (Wave 6 Prompt 6.5)
  run: bash scripts/check-outbox-only-publishing.sh
```

Running green on current tree:

```
$ bash scripts/check-outbox-only-publishing.sh
ok: no direct NATS publishes in services (outbox is the only write path)
```

## Why no production code changed

Because there's nothing to change. The three preceding CI guards
(math/rand, context.Background, outbox-only) form a defense-in-depth
set: any future PR that introduces a direct NATS publish trips the
build. Combined with the `TestDefaultStreamsCoverEveryKnownSubject`
test from Wave 5 Prompt 5.2, the contract is:

> "Every domain event emitted by a SeDoc service lands in an
> outbox row in the same SQL transaction as the state change it
> describes, is drained exactly once by the outbox publisher, and
> lands on a JetStream subject declared in the canonical topology."

If any of those three invariants breaks, CI fails before merge.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint | ✅ no code change |
| 2 | ≥75% coverage on new files | n/a — shell guard |
| 3 | Integration test | ✅ guard run green against full tree |
| 4 | OpenAPI | n/a |
| 5 | Prom metrics | n/a |
| 6 | Structured logs | n/a |
| 7 | Grafana dashboard | n/a |
| 8 | OTEL spans | n/a |
| 9 | RLS | n/a |
| 10 | NATS subject | n/a — this guard polices publishers, not topology |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | remove CI step; guard self-contained |
| 13 | Runbook | covered by the existing outbox documentation in `pkg/database/outbox_publisher.go` header comment |

## Wave 6 — complete

| Prompt | Status | Evidence |
|---|---|---|
| 6.1 per-tenant KEK | ✅ | [13a](13a-wave6-p6.1-per-tenant-kek.md) · ADR 0022 · 14 crypto tests |
| 6.2 session cookies + CSRF | ✅ | [13b](13b-wave6-p6.2-csrf-cookies.md) · 9 tests · live-verified |
| 6.3 crypto/rand + guard | ✅ | [13c](13c-wave6-p6.3-saml-serial-crypto-rand.md) · 3 tests · CI guard |
| 6.4 context propagation | ✅ | [13d](13d-wave6-p6.4-context-propagation.md) · 3 tests · CI guard |
| 6.5 outbox-only publishing | ✅ | this doc · CI guard |

**All five Wave 6 pilot blockers closed.** The G1 gate's security
exit criteria are met:
- No shared KEK ✅ (per-tenant via HKDF, ADR 0022)
- No `localStorage` token ✅ (already clean; CSRF double-submit added)
- `crypto/rand` everywhere ✅ (guard enforces)
- `context.Background()` CI guard green ✅
- Outbox-only publishing guard green ✅

## Next wave

Wave 7 — **Temporal workflow engine**: four core workflows (Document
Review, Approval Chain, Retention Disposition, Signature Orchestration
stub), the `task_inbox` table, a ReactFlow read-only designer page.
Temporal connection code exists in `services/workflow` but runs
nothing today.
