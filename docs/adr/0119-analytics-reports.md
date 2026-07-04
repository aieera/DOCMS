# ADR 0119 — Governed Analytics Query API + Saved/Scheduled Reports

- **Status**: Accepted
- **Date**: 2026-07-03
- **Relates to**: ADR 0085 (the Temporal-Schedule + reconcile pattern this
  mirrors), ADR 0086 (notification channels/digest), §4.7/C5 (outbox-only)

## Context

SeDoc's "analytics" were scattered single-purpose admin dashboards
(filing-analytics, anomaly reports, compliance) with no general way to
ask aggregate questions, save the question, or receive it on a
schedule. There is no separate analytics store — the store is the
domain tables, so the problem is exposing them SAFELY.

## Decision

### 1. Safety = a dataset registry, not a query language

`pkg/analytics` defines a whitelist of datasets (v1: `documents`,
`versions`, `tasks` — all document-service-owned), each declaring its
dimensions (groupable expressions), measures (aggregates), and
filterable fields. Clients submit only registry NAMES plus filter
values and an optional time range; the compiler assembles the SQL —
identifiers exclusively from the registry, values exclusively as bind
parameters, `tenant_id = $1` always compiled in — and execution happens
inside `WithTenantTx`, so RLS is the second tenant lock. Caps: ≤3
dimensions, ≤50 values per filter, LIMIT clamped to 1000. Golden tests
pin the exact SQL+args for representative specs (the DoD's
"hand-checked query" hook). The whole surface is **admin/owner-gated**:
tenant-wide aggregates (per-workspace counts, per-user activity) are
not member-level information.

### 2. Saved reports = stored specs; scheduling mirrors ADR 0085

`saved_reports` (document migration 000093, RLS-forced) stores the
query spec, a chart hint, delivery channels, and a schedule
(cron-wins-over-interval). REST: `/api/v1/analytics/{datasets,query,
reports…}` on the document-service mux (auth: admin at the gateway,
role-gated in the service). The workflow worker reconciles one Temporal
Schedule per enabled report (`scheduled-report-<id>`, 60s reconcile +
orphan sweep — the exact ADR 0085 pattern), so the document service
never needs a Temporal client.

### 3. Scheduled delivery = DB-side run + outbox events

The `DeliverScheduledReport` activity runs entirely against the shared
DB (no HTTP hop): loads the report, compiles via the SAME `pkg/analytics`
compiler the API uses, executes in the tenant tx, stamps `last_run_at`,
and outbox-emits `dms.report.generated.v1` (summary, new `dms.report.>`
binding on DOC_EVENTS) plus `dms.notify.report_ready.v1` — a
DeliveryPayload whose `channels` consent hint carries the report's
configured channels (in_app/email/digest), delivering a notification
with a deep link to the live report. Attaching the CSV to email and
filing runs as documents were considered and deferred (this option
reuses everything and satisfies "delivery within the frequency window").

### 4. UI: builder + chart + export

`/reports` (admin/owner nav): dataset picker, dimension/measure chips
(the registry's `datasets` endpoint drives them), filter rows, time
range, chart-type picker (table/bar/line/pie via recharts), saved-report
rail with schedule badges, save+schedule dialog (interval presets or
raw cron, channel checkboxes). Export: CSV client-side from the result
set; PDF via the browser print path.

## Consequences

- Adding a dimension/measure/dataset is a registry edit + golden test —
  no new endpoint surface.
- The compiler orders by the first measure DESC with a deterministic
  dimension tiebreak; explicit sort control is a follow-up.
- Charts render dimension-1 × measure-1; multi-measure rendering beyond
  the table is a follow-up.
- Cron expressions are passed to Temporal unvalidated (bad cron =
  schedule-create error surfaced in worker logs, not at save time) —
  validating at save is a cheap follow-up.
- No runtime E2E in this environment: compiler is golden-tested,
  services build + archtest green; the DB-backed run path needs the
  integration suite.
