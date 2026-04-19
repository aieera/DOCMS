# Performance baseline — YYYY-MM-DD

Fill this in after running `make all` under `tests/load/`. Spec
§13.2 DoD: every missed SLO has a tracked fix PR before this
doc gets merged.

## Run metadata

- **Date:** YYYY-MM-DD
- **Commit:** <git sha>
- **Cluster:** <staging cluster name>
- **Seed data:** 10 tenants × 500 documents (regenerate from
  `scripts/seed/` if different)
- **Runner:** <host or CI runner identity>
- **Wall clock:** ~90 minutes

## Summary

| SLO | Target | Observed | Pass? | Fix PR |
|---|---|---|---|---|
| Upload initiate p99 | < 200 ms | | ☐ | |
| Search p99 | < 300 ms | | ☐ | |
| OCR end-to-end p95 | < 30 s | | ☐ | |
| Permission check p99 | < 5 ms | | ☐ | |
| Auth login p99 | < 200 ms | | ☐ | |
| WS concurrent per pod | 1 000 | | ☐ | |
| Cross-tenant leaks | 0 | | ☐ | |

## Per-scenario details

### 01-document-crud

- Peak RPS: <>
- p50 / p95 / p99: <> / <> / <>
- Slow endpoints (top 5): <>
- Bottleneck: <DB? policy check? network?>
- Grafana screenshot: <link>

### 02-search

…

### 03-upload

…

### 04-ocr-pipeline

…

### 05-websocket

…

### 06-mixed-realistic

…

### 07-cross-tenant-isolation

- Leak count: **must be 0**. Any non-zero is a P0.

## Cost per 1 000 operations

| Operation | $/1 000 |
|---|---|
| Upload (incl. OCR) | |
| Search | |
| Document CRUD | |

## Bottlenecks + fixes

Enumerate every observed bottleneck, even ones that didn't
breach an SLO. File a follow-up PR per bottleneck.

| Bottleneck | Root cause | Fix PR |
|---|---|---|
| | | |

## Flamegraphs

Attach `go tool pprof` SVGs for the top-3 busiest services
during each scenario.
