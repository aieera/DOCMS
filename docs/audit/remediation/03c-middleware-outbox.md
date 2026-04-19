# Remediation 03c — Middleware chain, outbox pattern, NATS handler contexts

**Date:** 2026-04-17
**Scope:** Tenant-isolation gaps and durability gaps introduced by direct NATS
publishes and unscoped consumer contexts.
**Source findings:**
- `docs/audit/03-inconsistencies.md` §4 (middleware chain)
- `docs/audit/03-inconsistencies.md` §6 (outbox pattern)
- `docs/audit/04-antipatterns.md` §p (context.Background in NATS handlers)

---

## Task 1 — Billing gRPC interceptor chain

`services/billing/cmd/server/main.go` only installed
`RecoveryInterceptor`. Every other service installs the full
Correlation → Tenant → RequestLog chain. Expanded to match
[services/document/cmd/server/main.go:113-118](../../services/document/cmd/server/main.go):

```go
grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
    middleware.RecoveryInterceptor(log),
    middleware.CorrelationInterceptor(),
    middleware.TenantInterceptor(pool),
    middleware.RequestLogInterceptor(log),
))
```

Billing has no gRPC methods today, but the chain is required for the
moment a method is added — in particular `TenantInterceptor(pool)` is what
sets `app.current_tenant` on the connection and makes RLS policies fire.

---

## Task 2 — Direct NATS publishes → outbox

Four sites published directly to JetStream after a DB write committed.
Process crash between the commit and the publish loses the event. All
four now insert an `outbox` row inside the same transaction as the
business write; the existing `NewOutboxPublisher` goroutine polls the
table and forwards to NATS with delivery guarantees.

### Signature service (2 sites)

`services/signature/internal/service/service.go`:

- **CreateRequest** (was line 157)
  - Wrapped in `database.WithTenantTx`.
  - Repository gained a `Querier` interface (`Exec` method common to
    `*pgxpool.Pool` and `pgx.Tx`) and `CreateTx` / `CompleteTx` /
    `UpdateSignersTx` methods so the service can pass its own tx.
  - Inside the tx: `repo.CreateTx(tx, req)` then
    `outbox.Insert(tx, evt)` for subject `dms.notify.signature_requested.v1`.
- **RecordSignature** (was line 176)
  - Same pattern: `repo.UpdateSignersTx` + `repo.CompleteTx` +
    `outbox.Insert` for `dms.signature.completed.v1` when the request
    becomes complete.

Service struct now takes `Pool` and `Outbox *database.OutboxRepository`
in its `Config`; main.go wires `database.NewOutboxRepository()`. `JS`
dependency removed from the service — the outbox publisher owns NATS
access.

### Workflow activities (2 sites)

`services/workflow/internal/activities/activities.go`:

- **NotifyAssignee** — enqueues `dms.notify.workflow_assigned.v1` to
  outbox. Aggregate type `workflow_task`, aggregate id parsed from the
  document UUID (falls back to a fresh v7 if caller passes a non-UUID
  id, e.g. Temporal workflow handle).
- **PublishEvent** — enqueues the caller-supplied subject (e.g.
  `dms.workflow.completed.v1`). Aggregate type `workflow_instance`,
  aggregate id taken from `data["instance_id"]`.

`Activities` struct now holds `Outbox *database.OutboxRepository`
instead of `JS nats.JetStreamContext`; main.go wires it on worker
registration. The workflow service still constructs `nc, js, _ :=
events.ConnectNATS(...)` because `NewOutboxPublisher(pool, js, ...)`
needs the JS handle — just not the activities.

### Notification service — Redis pub/sub left alone

`services/notification/internal/service/service.go:59` uses
`rdb.Publish` for real-time WebSocket fan-out. Per the task's explicit
note, Redis pub/sub is best-effort by design; the in-app notification
row is already persisted on line 51 and is the source of truth. No
change.

---

## Task 3 — NATS handler context timeouts + correlation-id

Every NATS consumer callback now builds a per-message context rather
than passing `context.Background()` straight to the service layer:

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
if corrID := msg.Header.Get("correlation-id"); corrID != "" {
    ctx = auth.SetCorrelationID(ctx, corrID)
}
```

| Site | Timeout | Notes |
|------|---------|-------|
| `services/audit/internal/service/service.go:138` (`dms.>` consumer) | 30s | default |
| `services/connector/internal/service/service.go:82` (`dms.>` fanout) | 30s | shared ctx across lookup + inserts |
| `services/notification/internal/service/service.go:120` (`dms.notify.>`) | 15s | delivery latency budget |
| `services/search/internal/service/indexer.go` — 8 handlers | 30s | helper `handlerCtx(msg)` in the same file keeps the pattern DRY |
| `services/auth/internal/service/apikey.go:165` (debounced touch goroutine) | 5s | background, short circuit |
| `services/auth/internal/service/mfa.go:376` (KMS `GenerateDataKey`) | 10s | KMS SLO |

All remaining `context.Background()` in `services/` is in
`cmd/server/main.go` (signal handlers + shutdown ctx) or `_test.go`,
or it is nested inside `context.WithTimeout(context.Background(), ...)`
which is the intended pattern for a root of an async workflow.

### Correlation-id propagation

The outbox publisher attaches `correlation-id` as a NATS header when it
forwards an event to JetStream. Consumers now re-hydrate the id into
`auth.SetCorrelationID(ctx, …)`, so structured logs on the audit /
search / notification side carry the same id the original HTTP request
emitted. Events produced by upstream services that don't yet set the
header simply skip the branch.

---

## Task 4 — Outbox publisher coverage

Cross-check of services that insert outbox rows vs. services that run
the publisher goroutine:

| Service | writes outbox | runs publisher |
|---------|---------------|----------------|
| audit | no | no (consumer-only) |
| auth | yes | yes |
| billing | no | yes (ready for future use) |
| connector | no | yes |
| document | yes | yes |
| notification | no | yes |
| policy | yes | yes |
| search | no | yes |
| signature | yes | yes (new) |
| storage | yes | yes |
| workflow | yes | yes (new) |

No service writes to outbox without a publisher running. Audit is the
only service without a publisher, correctly — it is a pure subscriber
and never emits domain events.

---

## Verification

```bash
$ grep -rn "js\.Publish\|nc\.Publish" services/signature services/workflow
(no matches — all publishes go through outbox)

$ grep -rn "context\.Background()" services/ \
    | grep -v "cmd/server/main.go" \
    | grep -v "_test\.go" \
    | grep -v "context\.WithTimeout(context\.Background()"
(no matches — every consumer callback now uses a scoped ctx)

$ go build (every workspace module)
clean

$ go test pkg/... services/auth/... services/document/... \
           services/policy/... services/signature/... services/workflow/...
ok — all passing
```

The prompt's final live check (psql `SELECT event_type, published FROM
outbox WHERE event_type LIKE 'dms.signature%'`) requires a running
compose stack and was not executed in this session — the stack wiring
remains gated on remediation 09's follow-up OCR work. The code path
is verified:

- `OutboxRepository.Insert` is called inside the same transaction as
  the business write — visible in `CreateRequest` and `RecordSignature`.
- `NewOutboxPublisher(pool, js, serviceName, log)` + `go publisher.Start(ctx)`
  already in `services/signature/cmd/server/main.go` (lines 107-108)
  and `services/workflow/cmd/server/main.go` (lines 127-128).
- The publisher's own SQL (`pkg/database/outbox_publisher.go`) flips
  `published=true` after the NATS ack — unchanged by this remediation.

---

## DO-NOTs honored

- NATS subject names preserved verbatim
  (`dms.notify.signature_requested.v1`, `dms.signature.completed.v1`,
  `dms.notify.workflow_assigned.v1`, `dms.workflow.completed.v1`).
- No duplicate outbox rows — existing direct publishes were *rewritten*
  at the call site, not supplemented.
- Outbox polling interval untouched — this is a transport concern,
  separate from the write path.
- Notification Redis pub/sub left alone — it's a fan-out channel, not
  a durable event.
