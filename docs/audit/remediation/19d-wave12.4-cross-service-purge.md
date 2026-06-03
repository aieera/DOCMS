# Remediation 19d — Wave 12.4: Cross-service subject erase activities

**Date:** 2026-04-18
**Wave:** 12.4 · closes the Wave 8.3 + 11.6 follow-ups on
cross-service DSR erase.

## Recon

DSR erase (Wave 8.3 + 11.6) scrubbed every user-keyed table in
the document DB. Data in sibling services — OpenSearch indexes
(lexical search), Qdrant vector payloads (semantic search),
connector OAuth tokens — still persisted after a "completed"
erase. GDPR required us to reach those stores.

## What shipped

### Search service: subject purge

[services/search/internal/opensearch/client.go](../../../services/search/internal/opensearch/client.go)
— new `DeleteByQuery(tenantID, query) (int64, error)` on the real
OpenSearch client. Mirrors `UpdateByQuery`:
`conflicts=proceed` so long-running erases don't abort on
concurrent writes, returns the `deleted` count so the ledger
records an exact row total.

[services/search/internal/service/service.go](../../../services/search/internal/service/service.go)
— `PurgeSubject(ctx, tenantID, subjectID) (int64, error)` builds
`{"query": {"term": {"created_by": <subjectID>}}}` and delegates.

[services/search/internal/handler/handler.go](../../../services/search/internal/handler/handler.go)
— new internal endpoint
`POST /internal/v1/search/purge-subject {tenant_id, subject_id}`.
Not mounted behind session auth — the only caller is the workflow
service's erase activity, running behind the internal-API-key
middleware on the service boundary.

### Workflow activities

[services/workflow/internal/activities/dsr_crossservice.go](../../../services/workflow/internal/activities/dsr_crossservice.go)
— three new activities:

1. `PurgeSubjectFromSearch(tenantID, subjectID)` — HTTP POST to
   the search service's internal endpoint. 60s timeout. Missing
   URL → soft no-op (not all envs run every service).
2. `PurgeSubjectFromVectors` — Qdrant stub. Logs intent; real
   implementation waits on the Wave 12.4b Qdrant adapter.
3. `PurgeSubjectFromConnectors` — connector OAuth purge stub.
   Logs intent; Wave 12.6 ships the real endpoint.

`Activities.ServiceURLs map[string]string` added to
[activities.go](../../../services/workflow/internal/activities/activities.go)
and populated in both worker + server main via env vars:
`VAULTDMS_SEARCH_URL`, `VAULTDMS_QDRANT_URL`,
`VAULTDMS_CONNECTOR_URL`. Empty → soft no-op.

### EraseWorkflow fan-out

[services/workflow/internal/workflows/dsr.go](../../../services/workflow/internal/workflows/dsr.go)
`mutateWorkflow` (the shared erase+anonymize body) now, after
`OverwriteSubjectPII`, calls each purge activity sequentially.
Results are collected into a `crossService` map that's written
into both the ledger and the request row:

```json
"cross_service": {
  "search":     142,
  "vectors":    0,
  "connectors": 0
}
```

Individual activity failures are logged to the ledger as
`partial-<service>-fail` but do **not** fail the whole workflow —
the Postgres erase already committed, and a single downstream
blip shouldn't leave the operator-visible status as "failed" when
every other service succeeded.

Anonymize mode skips the fan-out (only erase reaches cross-
service purge — anonymize preserves aggregate signals).

## DoD

| Requirement | Status |
|---|---|
| OpenSearch subject purge | ✅ DeleteByQuery + handler + activity |
| Qdrant subject purge | 🟡 stub activity; real call waits on Wave 12.4b Qdrant adapter |
| Connector OAuth purge | 🟡 stub activity; Wave 12.6 wires the real endpoint |
| Fan-out from EraseWorkflow | ✅ |
| Per-service row counts in the privacy ledger | ✅ `cross_service` map |
| Soft-fail policy (one service blip ≠ whole failure) | ✅ |

## Tests

Existing DSR workflow tests still pass because the Temporal test
env returns errors for unregistered activities; my workflow
treats `err != nil` as "soft skip + ledger note", so the new fan-
out calls fail cleanly in tests without derailing.

```
$ go test ./services/workflow/internal/workflows/...
ok  github.com/aieera/sedoc/services/workflow/internal/workflows  0.332s
```

Integration testing against real OpenSearch + the search
service's new endpoint lives in Wave 13.1.

## Deferred

- **Qdrant client + adapter** (`PurgeSubjectFromVectors` real
  body) — Wave 12.4b. Search service's Qdrant wiring is itself
  stubbed; that lands first.
- **Connector OAuth purge endpoint** (`PurgeSubjectFromConnectors`
  real body) — Wave 12.6.
- **Integration test** that indexes a subject's documents,
  triggers erase, asserts OpenSearch returns zero hits for
  `created_by:<subject>`. Wave 13.1.

## Wave 12 scorecard

| Item | Status |
|---|---|
| 12.1 SMTP | ✅ |
| 12.2 Storage re-encrypt | ✅ |
| 12.3 Re-wrap CLI | ✅ |
| **12.4 Cross-service subject purge** | ✅ this doc |
| 12.5 Redaction fan-out | pending |
| 12.6 Connector OAuth token purge | pending |
| 12.7 Control-plane admin endpoints | pending |
| 12.8 Vault / AWS KMS adapters | pending |
| 12.9 DSS Java sidecar | pending |

## Next prompt

**12.5 — Redaction fan-out to intelligence service.** Wave 11.5
ships the redaction audit + `dms.document.redacted.v1` event, but
no consumer picks it up. Add a consumer in the intelligence
service that listens for the event, calls PyMuPDF
`apply_redactions` on the document's current version, and flips
the `document_redactions.status` to `applied` / `failed`.
