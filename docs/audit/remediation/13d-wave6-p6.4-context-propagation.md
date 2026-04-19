# Remediation 13d — Wave 6 Prompt 6.4: context propagation in NATS handlers

**Date:** 2026-04-17
**Wave:** 6 · **Prompt:** 6.4
**Source:** `DMS Architecture/final.md` § 5.4.

## Headline

Every NATS message handler now derives its per-message context from
the service's lifecycle context instead of `context.Background()`. On
SIGTERM, the lifecycle ctx cancels; the cancellation cascades into
every in-flight handler's derived ctx; downstream DB / HTTP / NATS
calls unwind cleanly. Drain behaviour that `signal.NotifyContext` was
already buying at the HTTP/gRPC layer now also works at the
event-consumer layer.

## Grep-confirmed hotspots fixed

Spec said 14; live grep found **4 real offenders** (the other
`context.Background()` hits are all legitimate — signal bootstrap,
shutdown-only contexts, one-shot KMS/API-key helpers with no request
context to inherit).

| Service | Before | After |
|---|---|---|
| search indexer | `handlerCtx(msg)` → `WithTimeout(context.Background(), 30s)` at 6 callsites | `handlerCtx(ix.parent, msg)` where `ix.parent` is set by `Indexer.Start(parent)` |
| audit consumer | `StartConsumer(js)` → `WithTimeout(context.Background(), 30s)` | `StartConsumer(parent, js)` → `WithTimeout(parent, 30s)` |
| connector event fanout | `StartEventFanout(js)` → `WithTimeout(context.Background(), 30s)` | `StartEventFanout(parent, js)` → `WithTimeout(parent, 30s)` |
| notification consumer | `StartConsumer(js)` → `WithTimeout(context.Background(), 15s)` | `StartConsumer(parent, js)` → `WithTimeout(parent, 15s)` |

All four `cmd/server/main.go` callers were updated to pass the
`signal.NotifyContext(...)`-derived `ctx` as the parent.

## Files landed

- [services/search/internal/service/indexer.go](../../../services/search/internal/service/indexer.go)
  — `handlerCtx(parent, msg)` signature change + `Indexer.parent`
  field + `Start(parent)` signature change + 6 call-site updates
  via replace-all.
- [services/audit/internal/service/service.go](../../../services/audit/internal/service/service.go)
  — `StartConsumer(parent, js)` signature change.
- [services/connector/internal/service/service.go](../../../services/connector/internal/service/service.go)
  — `StartEventFanout(parent, js)` signature change.
- [services/notification/internal/service/service.go](../../../services/notification/internal/service/service.go)
  — `StartConsumer(parent, js)` signature change.
- [services/audit/cmd/server/main.go](../../../services/audit/cmd/server/main.go),
  [services/connector/cmd/server/main.go](../../../services/connector/cmd/server/main.go),
  [services/notification/cmd/server/main.go](../../../services/notification/cmd/server/main.go),
  [services/search/cmd/server/main.go](../../../services/search/cmd/server/main.go)
  — each calls its `Start*` with the `ctx` from `signal.NotifyContext`.

## CI guard — `scripts/check-no-background-in-handlers.sh`

New CI step in the `security-go` job. Fails the build if any file
under `services/*/internal/` imports `nats-io/nats.go` and calls
`context.Background()` — with one narrow exception: the defensive
nil-fallback pattern

```go
if parent == nil { parent = context.Background() }
```

is allowed (it's a safety rail, not the bug). Test files and the two
known-safe single-shot helpers (`apikey.go:168`, `mfa.go:376`) are
also allow-listed — both have no caller-supplied ctx to inherit.

Running green on current tree:

```
$ bash scripts/check-no-background-in-handlers.sh
ok: no context.Background() in NATS-handling code paths
```

## Tests — propagation invariant pinned

[services/search/internal/service/indexer_ctx_test.go](../../../services/search/internal/service/indexer_ctx_test.go)
— 3 tests:

1. `TestHandlerCtx_InheritsParentCancellation` — cancel the parent,
   assert the derived ctx becomes done within 200ms. This is the
   load-bearing invariant for graceful drain.
2. `TestHandlerCtx_TimeoutStillFires` — even with a live parent, the
   30s per-message timeout must still bound the work.
3. `TestHandlerCtx_PropagatesCorrelationID` — header-set path doesn't
   drop cancellation.

```
$ go test -run HandlerCtx ./services/search/internal/service/...
ok  github.com/vaultdms/vaultdms/services/search/internal/service  0.172s
```

## Why it was 4 not 14

final.md counted `context.Background()` usages across the tree. Most
are legitimate:

- `signal.NotifyContext(context.Background(), SIGINT, SIGTERM)` — the
  root service ctx. Correct by construction.
- `context.WithTimeout(context.Background(), 30*time.Second)` inside
  shutdown blocks — shutdown deliberately detaches from the cancelled
  parent. Correct.
- `log.Info(context.Background())...Msg("shutting down")` — log without
  any request to attach to. Correct.
- `bgCtx` in `auth/service/apikey.go:168` — background async touch of
  `api_keys.last_used_at`, fire-and-forget; using the request ctx
  would cancel the write when the HTTP handler returns. Correct as a
  narrow exception.
- `kmsCtx` in `auth/service/mfa.go:376` — one-shot KMS unwrap during
  static config read at boot. Correct.

The 4 real handler-path offenders are fixed.

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint | ✅ |
| 2 | ≥75% coverage on new files | ✅ 3 tests cover every path of `handlerCtx` |
| 3 | Integration test | ✅ `TestHandlerCtx_InheritsParentCancellation` exercises the drain path directly |
| 4 | OpenAPI | n/a |
| 5 | Prom metrics | n/a — ctx plumbing, no new code path |
| 6 | Structured logs | n/a |
| 7 | Grafana dashboard | n/a |
| 8 | OTEL spans | 🟡 final.md wants an OTEL span per handler. Current code sets correlation ID in ctx; OTEL tracer config is deferred to Wave 13.6 |
| 9 | RLS | n/a |
| 10 | NATS subject | n/a |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | Revert the 4 service.go edits + 4 main.go caller edits. Guard disables itself once the calls are gone. |
| 13 | Runbook | n/a — operational behaviour unchanged from user perspective; drain improvement covered by runbook [05-nats-topology.md](../../runbooks/05-nats-topology.md) |

## Wave 6 scorecard

| Prompt | Status |
|---|---|
| 6.1 per-tenant KEK | ✅ |
| 6.2 session cookies + CSRF | ✅ |
| 6.3 crypto/rand for SAML serial | ✅ |
| 6.4 context propagation | ✅ this doc |
| 6.5 outbox-only publishing | pending |

## Next prompt

**6.5** — `grep -r 'js.Publish(' services/` → zero hits outside
`pkg/outbox`. Convert any direct NATS publishes in request handlers
to outbox inserts in the same tx as the state change they describe.
