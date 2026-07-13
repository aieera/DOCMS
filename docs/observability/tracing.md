# Distributed tracing

SeDoc emits [OpenTelemetry](https://opentelemetry.io) spans from every Go
service, the two Python workers (`intelligence`, `preview`), and the Node
`collaboration` service. Traces are exported over OTLP to a collector
(Jaeger all-in-one in local dev, Tempo in prod) and stitched into a single
end-to-end view — including the one hop that has no native instrumentation:
the async NATS boundary.

## The one non-obvious thing: tracing across the outbox

NATS/JetStream carries no OpenTelemetry instrumentation, so a naive setup
breaks every trace at the first async publish. SeDoc keeps traces connected
by riding the **transactional outbox**:

```
HTTP request (server span, otelhttp)
  └─ document write  ──┐
                       │  same tx
     outbox INSERT ────┘   ← pkg/database.OutboxRepository.Insert stamps the
       row.trace_context       request's W3C trace context onto the row
          │
          ▼  (async, polling publisher)
     OutboxPublisher.publishOne
          │  copies trace_context → NATS message `traceparent`/`tracestate` headers
          ▼
     NATS subject dms.*.v1
          │
          ▼
     search indexer handlerCtx
          └─ tracing.ConsumerContext extracts the headers → CONSUMER span
             is a CHILD of the producing request's span
```

The mechanism, end to end:

1. **`Insert`** (`pkg/database/outbox.go`) calls `tracing.InjectToMap(ctx)`
   and writes the resulting `traceparent`/`tracestate` map into the
   `outbox.trace_context` JSONB column (migration `000096`). Nullable — an
   event produced without an active span stores `NULL`.
2. **`OutboxPublisher.publishOne`** (`pkg/database/outbox_publisher.go`)
   copies that map onto the NATS message headers alongside `Nats-Msg-Id`.
3. **The consumer** (`services/search/internal/service/indexer.go`
   `handlerCtx`) rebuilds a carrier from the message headers and calls
   `tracing.ConsumerContext`, which extracts the context and starts a
   CONSUMER span parented to the original request.

This is why you can see `upload → document → outbox → search-indexer` as one
connected trace even though NATS sits in the middle.

## Enabling / configuration

Tracing is **off by default in code** so a service with no collector pays
nothing and never errors at boot. It turns on when either env var is set:

| Env var | Default (code) | Default (dev compose) | Meaning |
|---|---|---|---|
| `SEDOC_TRACING_ENABLED` | unset (off) | `1` | Force tracing on/off (`1`/`true` = on) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://tempo:4318` | `http://jaeger:4318` | OTLP/HTTP collector endpoint; setting it also enables tracing |
| `SEDOC_TRACE_SAMPLE_RATIO` | `0.01` (1%) | `1.0` (100%) | Head sampling ratio, `0.0`–`1.0` |

Sampling is **parent-based**: a request sampled at the edge keeps its whole
cross-service trace regardless of each service's local ratio
(`WithRemoteParentSampled(AlwaysSample)`), so end-to-end traces are never
half-captured. New roots are sampled at `SEDOC_TRACE_SAMPLE_RATIO`. Keep the
prod ratio low (1%) for cost; dev pins it to 100% so every request is
visible.

## View a trace locally

```bash
make docker-up            # brings up jaeger alongside the stack
./scripts/wait-for-healthy.sh
```

1. Open the Jaeger UI at <http://localhost:16686>.
2. Exercise the golden path — upload a document (via the web app, or the
   storage upload API). This produces `dms.version.uploaded.v1`, which the
   search indexer consumes.
3. In Jaeger, pick **Service: `document`** and **Find Traces**. Open the
   upload trace: you'll see the `document.http` server span, the outbox
   insert, and — linked through the NATS hop — the `search.index
   dms.version.uploaded.v1` consumer span, all under one trace ID.

If the search span shows up as its own separate root instead of a child,
the trace context didn't survive the outbox hop — check that migration
`000096` is applied (`SELECT trace_context FROM outbox LIMIT 1;`) and that
both services have `SEDOC_TRACING_ENABLED=1`.

## What is instrumented

| Boundary | Instrumentation |
|---|---|
| Inbound HTTP | `otelhttp.NewHandler` wraps each service's root handler → SERVER span, extracts inbound `traceparent` |
| Outbound / inbound gRPC | `otelgrpc` stats handler (per-service rollout; use `tracing`-installed global propagator) |
| Async NATS (outbox) | `trace_context` column → NATS headers → `tracing.ConsumerContext` (see above) |
| Custom spans | `tracing.StartSpan(ctx, name)` for OPA eval, S3, LLM, OCR hot spots |

The golden path (`document` HTTP + outbox → `search` consumer) is asserted
by a CI smoke test (`pkg/database/outbox_trace_smoke_test.go`, run under the
`integration` build tag) that starts a span, inserts an outbox row, runs the
real publisher against a live NATS, and asserts the same trace ID arrives on
the consumer side.

## Rolling out to a new service

Each Go `main()` needs three lines (already done for `document` and
`search`):

```go
if tracing.Enabled() {
    shutdown, err := tracing.Init(ctx, serviceName, cfg.ServiceVersion)
    if err == nil { defer func() { _ = shutdown(context.Background()) }() }
}
```

Then wrap the HTTP handler with `otelhttp.NewHandler(h, serviceName+".http")`
and, for any NATS consumer, build the per-message context with
`tracing.ConsumerContext(parent, headerMap, spanName)`. Producers get outbox
propagation for free — `OutboxRepository.Insert` already stamps the context.

### Python (`intelligence`, `preview`)

Install `opentelemetry-sdk` + `opentelemetry-exporter-otlp-proto-http` and,
at startup, configure the OTLP exporter against `OTEL_EXPORTER_OTLP_ENDPOINT`
with the W3C `tracecontext` propagator (the SDK default). Consumers extract
from the NATS message headers with `TraceContextTextMapPropagator().extract`;
the header names (`traceparent`/`tracestate`) match what the Go publisher
writes, so no custom format is needed.

### Node (`collaboration`)

Use `@opentelemetry/sdk-node` with `OTLPTraceExporter` from
`@opentelemetry/exporter-trace-otlp-http`. The default `W3CTraceContextPropagator`
is wire-compatible with the Go and Python sides.
