# ADR 0038 follow-up — full eDiscovery pipeline

**Builds on:** `ediscovery-foundation` branch (commits `4b32b47` → `6893cf3`).
**Status:** Plan, not yet started.
**Target branch name:** `ediscovery-pipeline`.

This document is the seam between the foundation PR (already pushed) and the production pipeline the spec asks for. It exists so whoever picks this up — current author or someone else — has a concrete, ordered work list with rationale and risk callouts. **It is not a commitment**; scope can shrink if priorities change.

## Why a separate PR

The foundation PR already touches 8 files across `proto/`, `services/storage`, `services/document`, and `docs/`. Adding the items below to the same PR would land **~25 more files**, including a new Temporal workflow, signature-service integration, two new HTTP route prefixes, and 6 new frontend routes. That's reviewable as a separate PR; not as one mega-PR on top of the foundation.

The foundation can merge to main on its own — it ships real value (matters table, EDRM XML, per-export verify with re-hash) without depending on anything below.

## Slices, in order

Each slice is one commit. Order matters: later slices depend on earlier schema, RPCs, and frontend wiring.

### F1 — Storage `UploadBundleRequest` RPC + 30-day presigned URL

**Files:** `proto/vaultdms/v1/storage.proto`, `services/storage/internal/service/service.go`, `services/storage/internal/handler/handler.go`, `services/document/cmd/server/main.go`.

The current export streams ZIP to the HTTP response and disappears. The async workflow writes the ZIP to S3 and returns a presigned GET URL. Add:

```proto
rpc UploadBundle(UploadBundleRequest) returns (UploadBundleResponse);
rpc PresignBundleDownload(PresignBundleDownloadRequest) returns (PresignBundleDownloadResponse);
```

`UploadBundle` writes to a dedicated bucket (e.g. `dms-ediscovery-bundles-<region>`) with object key `tenant/<id>/exports/<export_id>.zip`. `PresignBundleDownload` returns a 30-day presigned GET — checked against `ediscovery_exports.bundle_bucket/key`. Bucket lifecycle policy auto-deletes after 90 days unless the matter is open.

**Risk:** The ediscovery bucket needs separate IAM (compliance team only), separate KMS key, and separate retention policy. The follow-up runbook (F10) covers the AWS / MinIO setup.

### F2 — Async `EDiscoveryExportWorkflow` (Temporal)

**Files:** `services/workflow/internal/workflows/ediscovery.go` (new), `services/workflow/internal/activities/ediscovery.go` (new), `services/workflow/internal/handler/router.go` (register).

Workflow steps, each an activity:

1. `LoadExportRow` — fetch the `ediscovery_exports` row + `scope_json`.
2. `ResolveScope` — turn `scope_json` (document_ids[] OR folder_ids[] OR search_query) into a flat `[]uuid.UUID` of doc ids. Calls search service for query-scoped exports.
3. `BuildManifest` — calls a new internal HTTP endpoint on the document service (`POST /internal/v1/ediscovery/build-manifest`) that does what `ExportForDiscovery` does today but writes to a temp file instead of streaming. Returns the manifest hash.
4. `SignManifest` (slice F3) — calls signature service for detached PAdES.
5. `UploadBundle` (slice F1) — uploads the ZIP to S3.
6. `MarkComplete` — UPDATE `ediscovery_exports` SET status='completed', completed_at, manifest_sha256, bundle_bucket, bundle_key.
7. `EmitAudit` — emit `dms.ediscovery.export.completed.v1`.

Failure handling: per-step `temporal.RetryPolicy` (3 attempts, 5s initial, 1m max). Activity-level fail bubbles up to the workflow → `MarkFailed` activity that flips status='failed' + writes `error_summary`.

The existing synchronous `POST /admin/ediscovery/export` endpoint stays — operators that want a quick ZIP download keep using it. The new async path is `POST /admin/ediscovery/exports` with `{matter_id, scope}` body, returns 202 + `{export_id, status_url}`. Status polling via `GET /admin/ediscovery/exports/{id}` (which the foundation already exposes for verify; just adds a status field).

**Risk:** Pre-existing JetStream stream-filter issue blocked the workflow service in the disposition smoke tests. F2 needs that fixed first or workflow service won't drain its outbox. ~30 min runbook fix; not new code.

### F3 — PAdES manifest signing via signature service

**Files:** `services/signature/internal/service/sign.go` (extend), `services/workflow/internal/activities/ediscovery.go` (slice F2 calls into this).

Today's manifest signature is HMAC-SHA256. F3 swaps to detached PAdES via the existing `services/signature-signer/` JVM tool. Workflow activity:

1. POST manifest bytes to `services/signature` HTTP `/sign-detached`.
2. Signature service shells out to the signer JVM with the per-tenant code-signing cert.
3. Returns the `.p7s` detached signature.
4. Workflow writes `manifest.xml.sig` (PAdES format) into the bundle alongside `manifest.json.sig` (HMAC, kept for back-compat with vendors that already verify it).

The signer cert is per-tenant, stored encrypted in `tenant_kek`-wrapped form. Already-shipped infra; just plumbing.

**Risk:** Signer JVM container needs to be in compose for dev. It's listed in the compose file but probably not built locally. ~30 min docker build.

### F4 — Matter CRUD HTTP endpoints

**Files:** `services/document/internal/handler/ediscovery_matters_handler.go` (new), `services/document/cmd/server/main.go` (register).

```
POST   /api/v1/admin/ediscovery/matters                  create
GET    /api/v1/admin/ediscovery/matters                  list (paginated, ?status=open|closed|all)
GET    /api/v1/admin/ediscovery/matters/{id}             detail (joins custodians)
PATCH  /api/v1/admin/ediscovery/matters/{id}             update name/description
POST   /api/v1/admin/ediscovery/matters/{id}/close       sets closed_at
POST   /api/v1/admin/ediscovery/matters/{id}/reopen      clears closed_at
```

Schema is already in place from foundation E2. Just CRUD glue.

### F5 — Custodian add/remove endpoints

**Files:** same handler as F4.

```
POST   /api/v1/admin/ediscovery/matters/{id}/custodians         add user_id
DELETE /api/v1/admin/ediscovery/matters/{id}/custodians/{uid}   remove
```

Adding a custodian to an open matter should also fan out a notification (`dms.notify.ediscovery.custodian_added.v1`) so the user knows their docs are under hold preservation.

**Risk:** Should custodian removal be allowed while the matter is open? Probably no — custodians are append-only for legal-defensibility. Document the rule, return 409 on remove for open matters.

### F6 — Search-query scope resolution

**Files:** `services/workflow/internal/activities/ediscovery.go` (slice F2's `ResolveScope` activity).

Today's export takes `document_ids[]`. The follow-up takes any of:

```json
{
  "document_ids": ["..."],
  "folder_ids": ["..."],
  "search_query": {"q": "...", "filters": {...}, "facets": [...]}
}
```

`ResolveScope` activity:
- For `document_ids`: pass through.
- For `folder_ids`: recurse to all docs under each folder (`SELECT id FROM documents WHERE folder_id = ANY($1) AND deleted_at IS NULL`).
- For `search_query`: call `services/search` service with the query, paginate the results, collect doc ids. Cap at 10k docs per export to avoid runaway scopes; operators that need more split into multiple exports.

The persisted `scope_json` carries the raw scope shape (whatever the caller submitted), so reruns + audits can replay even if the underlying data shifted.

### F7 — OPA `ediscovery.export` permission code

**Files:** `services/policy/policy.rego` (extend), `services/document/internal/handler/ediscovery_handler.go` (replace hardcoded role check), `services/document/internal/handler/ediscovery_matters_handler.go` (new gate).

Today the role check is hardcoded `compliance_officer | admin | owner`. Move to OPA:

```rego
allow {
    input.action == "ediscovery.export"
    input.user.role == "compliance_officer"
}
allow {
    input.action == "ediscovery.export"
    input.user.role == "owner"
}
```

`admin` deliberately excluded from `ediscovery.export` per ADR 0038's separation-of-duties rationale (admins author retention policies; compliance officers approve evidence preservation). Operators who need both move to `owner`.

**Risk:** Existing legal-holds export dialog allows `admin` today. F7 narrows that. Surface in release notes. The `owner` role can always re-grant via a tenant-level OPA override if needed.

### F8 — `/ediscovery` admin frontend

**Files:** `web/src/api/ediscovery.ts` (new), `web/src/routes/_authenticated/admin/ediscovery.tsx` (new), `web/src/routes/_authenticated/admin/ediscovery/$matterId.tsx` (new).

Three routes:

- `/admin/ediscovery` — matter list (open/closed/all tabs), "+ New matter" button.
- `/admin/ediscovery/$matterId` — matter detail with: header (number, name, status), custodians table, scope picker, export history table.
- Export wizard inside the detail page — modal with three steps: pick scope (radio: documents | folder | search query), preview count, confirm. Submits to `POST /admin/ediscovery/exports`. Status polling shows pending → running → completed; click row to download bundle.

Verify-on-demand button on each completed export row → calls `POST /admin/ediscovery/exports/{id}/verify` → opens a panel showing per-doc check results (colored by mismatch / missing / ok).

**Risk:** The existing legal-holds export dialog at `/admin/legal-holds` should redirect to or co-exist with the new page. Don't remove it in this PR — operators will lose the muscle memory. Add a banner saying "matter-driven export available at /admin/ediscovery; this dialog kept for one-off exports."

### F9 — Integration tests

**Files:** `services/document/internal/service/ediscovery_integration_test.go` (new, build tag `integration`), `tests/fixtures/ediscovery/100docs/` (new).

Two tests, both spinning up Postgres + MinIO + storage service + document service via testcontainers:

1. **Happy path** — Create a matter, add 100 docs from fixture, export with `document_ids` scope, unzip the bundle, assert:
   - `manifest.json` parses; per-doc SHA matches recomputed SHA over `doc-<id>.json`'s referenced content
   - `manifest.xml` validates against the EDRM v1.2 XSD (XSD added to `docs/ediscovery/` as part of this slice; uses `xmllint --schema` via `os/exec` — gated by build tag because libxml2 isn't in every dev env)
   - `manifest.json.sig` verifies under the test signing key
   - `manifest.xml.sig` (PAdES from F3) validates via Tier-1 validator

2. **Tamper detection** — Same setup, then directly `UPDATE content_blobs SET sha256_hash = 'fake'` for one doc, run verify, assert `ok: false` with one mismatch entry.

### F10 — Runbook + Playwright

**Files:** `docs/runbooks/ediscovery-matter-workflow.md` (new), `web/e2e/28-ediscovery-export.spec.ts` (new).

Runbook covers: opening a matter, adding custodians, scoping the export, download workflow, verify-on-demand, what to do when verify reports drift, when to close a matter, escalation path.

Playwright covers: log in as compliance_officer, click /admin/ediscovery, create matter, add custodian, run an export, wait for status=completed, download bundle (verify Content-Disposition), click verify-integrity, see green check. Mock the long-running parts (workflow polling) by stubbing `GET /admin/ediscovery/exports/{id}` to flip from running → completed after 2 polls.

## Dependencies between slices

```
F1 (storage RPCs) ──┐
F2 (workflow)    ───┼──> F4 (matter CRUD) ──> F8 (frontend)
F3 (PAdES)        ──┘                     ──> F9 (integration tests)
F5 (custodians)   ──> F8
F6 (scope)        ──> F2
F7 (OPA)          ──> F4
F10 (runbook)     ──> ships last
```

F1 / F3 / F5 / F6 / F7 are independent; can be parallelized.
F2 depends on F1 + F3 + F6.
F4 depends on F7.
F8 depends on F4 + F5.
F9 depends on F2 + F3.
F10 ships last.

## Estimated size

| Slice | Files | Approx LOC | Risk |
|---|---|---|---|
| F1 | 4 | ~250 | low — mirrors existing ShredBlobs/HashBlob pattern |
| F2 | 3 | ~400 | medium — Temporal workflow + activities + JetStream prereq |
| F3 | 2 | ~200 | medium — signer JVM container + per-tenant cert plumbing |
| F4 | 2 | ~300 | low — pure CRUD |
| F5 | 1 | ~150 | low — extends F4 handler |
| F6 | 1 | ~250 | medium — search-service integration |
| F7 | 3 | ~150 | low |
| F8 | 3 | ~600 | medium — three routes + wizard + verify panel |
| F9 | 2 | ~400 (test) | high — testcontainers + XSD + libxml2 + 100-doc fixture |
| F10 | 2 | ~500 (mostly markdown + e2e) | low |
| **Total** | **23** | **~3200** | |

Roughly 2–3 days of focused work for a single engineer who already understands the foundation PR. Half a week if F2 has Temporal teething problems.

## What remains explicitly deferred

Even after F1–F10, these stay deferred:

- **Multi-region bundle storage** — the bundle bucket is single-region per tenant. A US tenant generating an EU-pinned matter export gets a US bucket, which is fine for delivery but problematic if EU residency requires the bundle stay in EU. ADR 0034 region governance applies in spirit; an explicit policy decision lands later.
- **Production-scale fixture** — F9 uses 100 docs. A million-doc matter export is a different problem (streaming manifest generation, multipart upload, time-bounded workflow). Out of scope; documented as a follow-up after a real customer asks.
- **Custodian self-service portal** — when a custodian is added to a matter, they get notified but have no UI to acknowledge or upload supplemental documents. ADR-level decision: do we want one? Today's answer is "no, custodians work through their compliance team."
- **Matter linking to legal_holds** — a matter often corresponds to a legal hold; today they're separate tables with a free-text matter_reference link. A follow-up could enforce a foreign key or join column. Not pressing — operators copy/paste matter numbers between the two surfaces today and that works fine.

## How to start

Once `ediscovery-foundation` merges to main:

```bash
git checkout main
git pull
git checkout -b ediscovery-pipeline
# Start with F7 (smallest, unblocks F4) or F1 (smallest cross-service, unblocks F2)
```

Don't try to do all of F1–F10 in one branch. Stack them: open a PR after F1+F4+F7 (a coherent slice — async + matter CRUD + auth gate), then a second PR for F2+F3+F6 (workflow + signing + scope), then a third for F8+F9+F10 (frontend + tests + runbook). Three PRs of ~8 commits each is reviewable; one PR of 25 commits is not.
