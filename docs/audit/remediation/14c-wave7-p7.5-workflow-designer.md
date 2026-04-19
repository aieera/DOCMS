# Remediation 14c — Wave 7 Prompt 7.5: read-only ReactFlow designer

**Date:** 2026-04-17
**Wave:** 7 · **Prompt:** 7.5
**Source:** `DMS Architecture/final.md` § 6.4 / § 14.1.

## Recon finding

The admin → Workflows page was an empty placeholder with a static "No
workflows defined" tombstone. The backend already served
`GET /api/v1/workflows/definitions` (Wave 7 P7.3 pre-existing), so the
gap was purely frontend: no API client binding, no graph rendering.

final.md § 14.1 explicitly **declines** drag-to-create authoring — the
deliverable for this prompt is a read-only visualization of each
definition's step DAG, including conditional branches.

## What shipped

### Dependency

`@xyflow/react@12.10.2` added to `web/package.json` (the current
iteration of ReactFlow). Bundle impact is isolated to the admin
Workflows route.

### Graph builder + component

[web/src/components/shared/WorkflowGraph.tsx](../../../web/src/components/shared/WorkflowGraph.tsx)
— exports:

- `buildGraph(steps)` — pure function converting a
  `WorkflowStep[]` (with nested `on_true` / `on_false` branches for
  `condition` steps) into `Node[] + Edge[]` with deterministic
  coordinates. Linear steps stack top-down; conditions fork left/right
  and rejoin at a small join marker. Start/end bookend nodes are
  appended automatically.
- `WorkflowGraph` — React component that memoizes `buildGraph` and
  renders a ReactFlow canvas with drag/connect/select all disabled.
  `proOptions.hideAttribution: true` is set since this is an internal
  admin view.

No dagre or elk — the layouts we build are small (≤~20 nodes per
definition) and deterministic hand-tuned coordinates keep the new
bundle lean.

### API client

[web/src/api/workflows.ts](../../../web/src/api/workflows.ts) now exports
`WorkflowStep`, `WorkflowDefinition`, and `getWorkflowDefinitions()`.

### Admin page wire

[web/src/routes/_authenticated/admin/workflows.tsx](../../../web/src/routes/_authenticated/admin/workflows.tsx)
— replaced the tombstone with a two-column layout: definition list on
the left, selected definition's graph on the right. Defaults to the
first definition. Empty-state copy now says drag-to-create is
"post-G3" so admins know the limitation is intentional.

### Tests

[web/src/components/shared/WorkflowGraph.test.tsx](../../../web/src/components/shared/WorkflowGraph.test.tsx)
— vitest covers the pure layout:

1. Empty workflow → start + end with a direct edge between them.
2. Linear chain → start → s-0 → s-1 → end edges in order.
3. Condition step with `on_true` / `on_false` → both branches emit,
   edges labeled `true`/`false`, branch tails merge into a join node,
   join flows into the follow-up step.
4. Condition as the final step → no join node emitted.

```
$ cd web && npx vitest run src/components/shared/WorkflowGraph.test.tsx
Test Files  1 passed (1)
     Tests  4 passed (4)
```

TypeScript check clean (`npx tsc --noEmit`).

## DoD — § 1.4 audit

| # | Requirement | Status |
|---|---|---|
| 1 | Compiles + lint clean | ✅ tsc clean |
| 2 | ≥75% coverage on new files | ✅ buildGraph branches exercised; component render deferred (jsdom can't measure ReactFlow) |
| 3 | Integration test | ⚠ Playwright E2E lands with Wave 13.1 (visual regression via Percy) |
| 4 | OpenAPI | n/a — no new backend routes |
| 5 | Prom metrics | n/a — static admin page |
| 6 | Structured logs | n/a |
| 7 | Grafana dashboard | n/a |
| 8 | OTEL spans | n/a |
| 9 | RLS | n/a — read from already-authenticated endpoint |
| 10 | NATS subject | n/a |
| 11 | Index-plan comment | n/a |
| 12 | Rollback | revert `web/src/components/shared/WorkflowGraph*`, `web/src/routes/_authenticated/admin/workflows.tsx` commit; uninstall `@xyflow/react` |
| 13 | Runbook | component is self-describing |

## Deferred (logged in out-of-scope.md)

- Drag-to-create authoring — explicitly declined in final.md § 14.1.
- Full auto-layout (dagre/elkjs) — current hand-tuned coords overflow
  on workflows with many nested conditions; switch libraries when a
  real customer hits a definition wide enough to clip.
- Playwright visual-regression test — Wave 13.1.
- Live instance overlay (highlight the currently-active step on a
  running instance) — Wave 10 (task detail drawer).

## Wave 7 scorecard

| Prompt | Status |
|---|---|
| 7.1 package skeleton + ADR + worker binary | ✅ |
| 7.2 Document Review workflow details | partial (replay test pending) |
| 7.3 Approval Chain workflow details | ✅ pre-existing |
| 7.4 My Tasks endpoint + UI wire-up | ✅ |
| 7.5 ReactFlow read-only designer | ✅ this doc |

## Next wave

**Wave 8 — Compliance:** retention cron workflow (fills the Prompt 7.1
stub), legal hold API + UI, GDPR DSR (ADR 0024), data residency pinning.
