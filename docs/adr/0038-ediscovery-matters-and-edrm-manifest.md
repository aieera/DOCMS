# ADR 0038 — eDiscovery matters, EDRM XML manifests, and verify-on-demand

**Status:** Proposed · **Date:** 2026-04-26 · **Builds on:** Wave 8.5 / G9 eDiscovery export ([`services/document/internal/service/ediscovery.go`](../../services/document/internal/service/ediscovery.go), [`services/document/internal/handler/ediscovery_handler.go`](../../services/document/internal/handler/ediscovery_handler.go)).

## Context

`POST /api/v1/admin/ediscovery/export` already streams a ZIP carrying a JSON manifest with per-doc SHA-256 + an HMAC-SHA256 signature, and emits `dms.ediscovery.exported.v1`. Three things make that flow inadequate for an actual matter-driven legal hold workflow:

1. **No persistent matter or export log.** Each export is a one-shot HTTP call; the request body carries `case_id` + `case_name` as free strings. Two exports for the same matter can disagree on the matter name; nothing ties them together for audit. The legal hold table has `matter_reference` (string), but there is no `ediscovery_matters` record with a matter number, opened-by, opened-at, closed-at, or per-matter custodian list.
2. **JSON manifest, not EDRM.** Outside counsel and discovery vendors expect EDRM XML (Electronic Discovery Reference Model v1.2). A JSON manifest fails their import tools out of the box; legal teams have to hand-translate.
3. **No verify-on-demand for an existing bundle.** Today the audit service exposes `POST /api/v1/audit/verify-integrity`, but that walks the *tenant-wide* hash chain. Defending the integrity of a specific 90-day-old export (the one a regulator just asked about) means recomputing per-doc SHAs against the bytes in the bundle plus the manifest signature. There is no endpoint for it.

These are real gaps, but the existing flow's primitives are sound. **Augment in place.** The Wave 8.5 ZIP + HMAC + audit emit stay; this ADR adds a matter/export log, an EDRM-shaped manifest emitted alongside the JSON, and a per-export verify endpoint.

The full spec (PAdES manifest signing, async Temporal workflow, 30-day presigned URL, `/ediscovery` admin frontend, OPA `ediscovery.export` permission, integration tests) is substantially larger and lands in a follow-up PR.

## Decision

### Schema (migration `000018_ediscovery_matters`)

Three new tables + one back-fill column on the existing audit chain:

```sql
CREATE TABLE ediscovery_matters (
    tenant_id      UUID NOT NULL REFERENCES organizations(id),
    id             UUID NOT NULL DEFAULT gen_random_uuid(),
    matter_number  TEXT NOT NULL,        -- operator-assigned, unique per tenant
    name           TEXT NOT NULL,
    description    TEXT,
    created_by     UUID NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at      TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, id),
    UNIQUE (tenant_id, matter_number)
);

CREATE TABLE ediscovery_custodians (
    tenant_id  UUID NOT NULL REFERENCES organizations(id),
    matter_id  UUID NOT NULL,
    user_id    UUID NOT NULL,
    added_by   UUID NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, matter_id, user_id),
    FOREIGN KEY (tenant_id, matter_id) REFERENCES ediscovery_matters(tenant_id, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, user_id)   REFERENCES users(tenant_id, id)
);

CREATE TABLE ediscovery_exports (
    tenant_id        UUID NOT NULL REFERENCES organizations(id),
    id               UUID NOT NULL DEFAULT gen_random_uuid(),
    matter_id        UUID NOT NULL,
    requested_by     UUID NOT NULL,
    -- JSON: the resolved scope as produced. {document_ids:[...], folder_ids:[...],
    -- search_query:{...}}. Persisted so reruns + audits can replay the same set.
    scope_json       JSONB NOT NULL,
    -- 'pending' | 'running' | 'completed' | 'failed'. Today the
    -- existing synchronous handler always lands rows in 'completed' (it
    -- streams ZIP back atomically); the async workflow follow-up will
    -- use pending → running → completed.
    status           TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at     TIMESTAMPTZ,
    -- Manifest digest (SHA-256 of the canonical JSON manifest bytes).
    -- Recorded for verify-on-demand: a verify call recomputes it.
    manifest_sha256  TEXT,
    -- Storage location of the bundle (when async upload follow-up
    -- ships). NULL today because the existing flow streams ZIP to
    -- the HTTP response without persisting.
    bundle_bucket    TEXT,
    bundle_key       TEXT,
    bundle_size_bytes BIGINT,
    -- Operator-supplied note (e.g. case context for the audit).
    note             TEXT,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, matter_id) REFERENCES ediscovery_matters(tenant_id, id)
);
CREATE INDEX idx_ediscovery_exports_matter ON ediscovery_exports(tenant_id, matter_id, started_at DESC);
```

RLS enabled on all three. The existing `ediscovery_handler` keeps working unchanged — but now writes an `ediscovery_exports` row per call so the chain-of-custody trail exists even before the async workflow lands.

### EDRM XML manifest

Today's JSON manifest stays — discovery tooling that already consumes it keeps working. Alongside it the bundle gains `manifest.xml` produced from the same source-of-truth Go struct (`DiscoveryManifest`) so the same export ships both formats. The EDRM XML template lives at [`docs/ediscovery/edrm-schema.xml`](../../docs/ediscovery/edrm-schema.xml) — a v1.2 conformant skeleton with substitution tokens.

The Go serializer renders into a `LoadFile` element matching the EDRM v1.2 schema — minimum viable, just enough for a vendor's `loadfile-import` tool to ingest. Optional EDRM extensions (custom metadata, page-level extraction) are documented but not emitted in v1; legal teams typically request additions per-case and the schema accommodates them with `<UserDefinedField>`.

### Per-export verify endpoint

`POST /api/v1/admin/ediscovery/exports/{id}/verify` (compliance_officer | admin | owner gated, same as the export endpoint):

1. Look up the `ediscovery_exports` row → fetch `manifest_sha256` and `scope_json`.
2. Re-resolve the scope to its document set.
3. For each document, recompute the *current* `content_sha256` from the storage layer.
4. Compare each against the manifest's per-doc hash.
5. Return `{ok: bool, verified_count, mismatches: [{doc_id, expected_sha, current_sha}]}`.

Two distinct kinds of mismatch:
- **Manifest drift** — a doc's current SHA differs from the manifest. Either the bytes changed (which they shouldn't — content blobs are immutable) or the manifest was tampered with after creation.
- **Missing doc** — a doc in scope was deleted post-export. Expected for old matters; verify reports it without failing the overall result unless the caller passes `?strict=true`.

The endpoint does NOT recompute the manifest signature (no PAdES yet — that's the follow-up). It re-runs the existing HMAC validation if the bundle is available. v1 verify is meaningful even with HMAC because the existing signing key + audit trail proves the manifest's authenticity at the time it was emitted.

### What we did not do

- **No async Temporal workflow.** Existing sync handler keeps shipping ZIPs. The follow-up PR's `EDiscoveryExportWorkflow` will write rows in `pending` and progress them through `running` → `completed`; the schema accommodates both flows.
- **No PAdES/CAdES manifest signing.** HMAC-SHA256 stays for now. Follow-up integrates `services/signature/` for real detached PAdES.
- **No 30-day presigned-URL bundle storage.** Bundle still streams to the HTTP response. `ediscovery_exports.bundle_bucket/key` is reserved for the follow-up.
- **No `ediscovery.export` OPA permission code.** Hardcoded role check (`compliance_officer | admin | owner`) stays through this PR. Adding an OPA capability is one rule, but threading it through requires a policy.rego change + tests; defer to keep this PR focused.
- **No standalone `/ediscovery` admin frontend.** The existing dialog inside `/admin/legal-holds` keeps working. The matter list + custodian editor + export wizard frontend is the bulk of the follow-up PR.
- **No integration tests.** Integration tests for the export flow are a meaningful piece of work (need a 100-doc fixture, ZIP-roundtrip checks, tamper detection). Listed in the follow-up.
- **No deprecation of the legacy `case_id`/`case_name` request body.** The existing handler accepts the old shape AND now ALSO accepts a `matter_id` UUID that maps to an `ediscovery_matters` row. Operators can migrate gradually.

## Consequences

**Positive**
- Every export now has a queryable audit row in `ediscovery_exports` with the full scope JSON, manifest hash, and timestamps. "What did we ship to outside counsel last May?" becomes a single SELECT.
- EDRM XML output unblocks matter intake at most discovery vendors without hand-translation.
- Verify-on-demand answers a regulator's "prove this export hasn't been tampered with" question without re-running the export.
- Zero churn for already-shipped legal-holds export dialog. Existing JSON manifest output stays bit-identical.

**Negative**
- Matters and exports are now first-class persistent entities — operators that used to fire-and-forget exports must learn the matter-first workflow when the follow-up frontend lands. v1 hides this: the existing endpoint still accepts the `case_id` shape and synthesises a matter row when one doesn't exist.
- Two manifest formats (JSON + EDRM XML) double the surface to keep in sync. The single Go struct → two serializers pattern keeps the drift cost low; documented in the runbook follow-up.
- `verify` doesn't yet detect every tamper class — without PAdES it's HMAC-only. Documented as a follow-up alongside the signature service integration.

## References

- Wave 8.5 / G9 eDiscovery export — [`services/document/internal/service/ediscovery.go`](../../services/document/internal/service/ediscovery.go)
- ADR 0035 (legal hold §9.3) — augment-in-place precedent
- ADR 0027 (acknowledgement attestation chain) — per-aggregate hash chain pattern reused for the matter-level audit
- EDRM v1.2 spec — http://www.edrm.net/resources/standards/edrm-xml/
