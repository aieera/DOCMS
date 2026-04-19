# Chaos 01 — Pod kill

## Premise

Kill one pod every 10 minutes across every service for a full
hour. Pilot-grade availability claim: no user-visible error,
p99 latency ceiling breached for < 30 s per kill.

## Setup

- Chaos-mesh CRD: `PodChaos` with `action: pod-kill`,
  `mode: one`, `duration: 10m`, selector on each deployment.
- k6 in the background driving 200 docs/min + 100 q/s against
  the staging cluster so the kill lands during real traffic.

## Verification

Grafana dashboards to watch:

- `storage.request_duration_seconds{quantile="0.99"}` — must
  stay below 1 s averaged over any 60-s window.
- `http_requests_total{code=~"5.."}` — no more than 2% of
  requests in the minute following a kill.
- Pod restart count (kubectl events): every killed pod back in
  `Running` within 30 s.

## Expected behaviour

- Kubernetes readiness + liveness probes kick in; the affected
  pod's replica set spawns a replacement.
- Upstream services (e.g. document → storage) retry idempotent
  GRPC calls; k6 sees a momentary error budget burn that
  recovers before the next kill.
- NATS + Postgres connections re-establish cleanly.

## Expected failure modes (log but don't fail)

- A single 503 on the in-flight request at the instant of the
  kill. This is acceptable — k6 retries.

## Fail-the-test triggers

- Any pod that doesn't come back within 2 min.
- p99 stays above 1 s for more than 120 s total across the hour.
- 5xx rate exceeds 2% in any 60-s window.

## Post-mortem template

See [../post-mortem-TEMPLATE.md](../post-mortem-TEMPLATE.md).
