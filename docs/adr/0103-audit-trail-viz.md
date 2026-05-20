# ADR 0103 — Audit trail visualization

Status: Accepted (Phase 1 on-the-fly aggregation shipped; precomputed
read-model deferred to Phase 2)
Date: 2026-05-19
Related: §18 Feature 10 of the blueprint; existing audit service
(`services/audit`), ADR 0066 (comments), ADR 0067 (annotations),
ADR 0074 (GraphQL doc-detail merge — the existing Activity feed).

## Context

The Activity tab on a document detail page is a flat, chronological
feed today (the existing `ActivityFeed` component, ADR 0074). Useful
for "what happened in order" but bad for "who's been most active",
"which actions are common", "what time of day do edits cluster."

§18 F10 asks for three viz overlays:

1. **Vertical timeline** — like today's feed but with sparkline density.
2. **Sankey actors → actions** — see who did what at a glance.
3. **Heatmap by hour-of-day** — when does activity cluster.

Plus filters by user and action type.

The data already exists in `audit_events` (partitioned monthly,
populated by every service via the audit gRPC client). The new work is:

- One aggregation endpoint that returns the three views from a single
  query pass.
- Frontend renderers for Sankey + heatmap (timeline already exists).
- Filter wiring on top.

## What is shipped now (Phase 1)

- This ADR.
- `GET /api/v1/audit/documents/{document_id}/viz?bucket=hour|day&since=…`
  on the audit service. Returns:
  ```json
  {
    "time_buckets":  [{ "ts": "2026-05-19T08:00:00Z", "count": 7 }, ...],
    "actors":        [{ "id": "<uuid>", "name": "Admin", "count": 12 }, ...],
    "actions":       [{ "name": "document.updated", "count": 8 }, ...],
    "sankey_edges":  [{ "actor": "<id>", "action": "document.updated", "count": 5 }, ...],
    "heatmap":       [[0,0,0,...,4,7,5,...,0], [...]],
    "total_events":  42
  }
  ```
  Single SQL pass over the resource's audit_events; tenant-scoped via
  RLS (audit_events already has the policy in place).
- Frontend client + viz component:
  - `web/src/api/auditViz.ts`
  - `web/src/components/documents/AuditVisualization.tsx` — three
    panels (Sankey, heatmap, top actors/actions bars) rendered with
    plain SVG to avoid a new dependency.
- Activity tab: toggle between "Feed" (existing chronological view)
  and "Insights" (the new viz).

## What is **not** shipped now

- **Precomputed read-model.** The aggregation runs from `audit_events`
  on every request. For a doc with thousands of events this could get
  slow (~200ms+); Phase 2 adds an `audit_doc_viz_cache` table
  materialized hourly or on event-publish. The endpoint signature
  doesn't change — we just swap the data source.
- **Real Sankey via d3-sankey.** Phase 1 ships a simplified
  actor-action grouped-bar layout that conveys the same information
  without a 60kB dep. A real d3 Sankey is Phase 2 polish.
- **Filter chips persistence.** Filters are URL-only in Phase 1; Phase
  2 stores user-selected views as saved filters.
- **Playwright** for the new viz interactions.

## Aggregation SQL (Phase 1)

One query, three result CTEs:

```sql
WITH base AS (
  SELECT ae.actor, ae.actor_name, ae.action, ae.created_at
    FROM audit_events ae
   WHERE ae.tenant_id = $1
     AND ae.resource_type = 'document'
     AND ae.resource_id   = $2
     AND ae.created_at   >= $3       -- since
),
by_time AS (
  SELECT date_trunc($4, created_at) AS ts, COUNT(*) AS count
    FROM base GROUP BY 1
),
by_actor AS (
  SELECT actor, MIN(actor_name) AS name, COUNT(*) AS count
    FROM base GROUP BY actor
),
by_action AS (
  SELECT action, COUNT(*) AS count
    FROM base GROUP BY action
),
sankey AS (
  SELECT actor, action, COUNT(*) AS count
    FROM base GROUP BY actor, action
),
heatmap AS (
  SELECT EXTRACT(DOW  FROM created_at)::int AS dow,
         EXTRACT(HOUR FROM created_at)::int AS hr,
         COUNT(*) AS count
    FROM base GROUP BY dow, hr
)
SELECT json_build_object(
  'time_buckets',  (SELECT json_agg(json_build_object('ts', ts, 'count', count) ORDER BY ts) FROM by_time),
  'actors',        (SELECT json_agg(json_build_object('id', actor, 'name', name, 'count', count) ORDER BY count DESC) FROM by_actor),
  'actions',       (SELECT json_agg(json_build_object('name', action, 'count', count) ORDER BY count DESC) FROM by_action),
  'sankey_edges',  (SELECT json_agg(json_build_object('actor', actor, 'action', action, 'count', count) ORDER BY count DESC) FROM sankey),
  'heatmap',       (SELECT json_agg(json_build_object('dow', dow, 'hour', hr, 'count', count)) FROM heatmap),
  'total_events',  (SELECT COUNT(*) FROM base)
);
```

`audit_events` is monthly-partitioned + has
`idx_audit_events_resource` on `(tenant_id, resource_id)`; query
plans to a partition-bounded index scan + light aggregation. Fast
enough for typical document audit sizes.

## Frontend

`AuditVisualization.tsx` (no new deps):

```
┌─────────────────────────────────────────────────────────┐
│ Time buckets (sparkline)                                │
│ ▁▂▁▃▁▂▅▇▃▁▂                                              │
├──────────────────────┬──────────────────────────────────┤
│ Top actors           │ Top actions                      │
│ ▓▓▓▓▓▓▓▓▓▓ alice 12  │ ▓▓▓▓▓▓▓▓ document.updated 8      │
│ ▓▓▓▓▓ bob 5          │ ▓▓▓▓ document.tag_added  4       │
├──────────────────────┴──────────────────────────────────┤
│ Activity by hour-of-day (heatmap, 24×7)                 │
│ Mon │░ ░ ░ ░ ░ ░ ░ ░ ░ ░ ░ ░ ▓ ▒ ▒ ░ ░ ░ ░ ░ ░ ░ ░ ░│  │
│ Tue │░ ░ ░ ░ ░ ░ ░ ░ ░ ▒ ▓ ▓ ▒ ░ ░ ░ ░ ░ ░ ░ ░ ░ ░ ░│  │
│  …                                                       │
└─────────────────────────────────────────────────────────┘
```

- Sparkline: 1-pixel-wide bars, scaled by max count in the window.
- Bars: clickable for filter ("show only this actor / action").
- Heatmap: 24×7 grid, opacity scales with count, hover shows count.

Filters propagate as URL query params (`?actor=<id>&action=…`) so
the view is shareable.

## Open questions deferred

- **Cross-resource viz.** "Show the audit pattern for this whole
  workspace, not just this doc." Same shape, just different `WHERE`.
  Phase 2 when users actually ask for it.
- **Time zones.** Heatmap is in UTC today; the user probably wants
  local time. Phase 2 — needs the user's TZ from profile + a
  hour-of-day-shift pass on the server response.
- **Privacy.** The `actor_name` column is denormalized; if a user is
  deleted under GDPR, the audit row keeps the historical name. That's
  by design (legal hold) but UI should label deleted users as such.
  Today it doesn't. Phase 2 — join against `users` to flag deleted.
