# VaultDMS

Enterprise Document Management System. Multi-tenant, region-aware, policy-enforced.

## Stack

| Layer          | Tech                                  |
|----------------|---------------------------------------|
| Backend        | Go 1.22+ (workspaces, gRPC, REST)     |
| Frontend       | React 18 + TypeScript + Vite          |
| Database       | PostgreSQL 16 (RLS for tenant isolation) |
| Search         | OpenSearch 2.12                       |
| Vector DB      | Qdrant 1.7                            |
| Cache          | Redis 7                               |
| Object Storage | MinIO (S3-compatible)                 |
| Event Bus      | NATS 2.10 + JetStream                 |
| Workflow       | Temporal                              |
| Auth Policy    | OPA (embedded)                        |

## Repository layout

```
vaultdms/
  proto/              # Protobuf contracts (source of truth for APIs)
  pkg/                # Shared Go libraries (config, logger, middleware, ...)
  services/           # One Go module per microservice
    document/
    storage/
    search/
    auth/
    policy/
    workflow/
    notification/
    audit/
    signature/
    billing/
    connector/
  scripts/            # DB init, seeders, tooling
  .github/workflows/  # CI + release
  docker-compose.yml  # Local dev: all dependencies + services
```

## Quickstart

```bash
# 1a. Fast path: pull pre-built images from ghcr.io/aieera/docms
#     (populated on every merge to main by .github/workflows/images.yml).
#     ~2 minutes of pulls vs. ~45 minutes of cold builds.
make docker-up-prebuilt

# 1b. Build-from-source path. 24 services (10 infra + 3 app + 11 Go),
#     every long-running container with a healthcheck.
make docker-up

# 2. Block until every compose service reports healthy (≤120s):
./scripts/wait-for-healthy.sh

# 3. (Optional) run Go services on the host instead of in containers.
#     You cannot do both at once — host ports would collide.
docker compose stop auth policy document storage search audit \
                    workflow notification signature billing connector
make run-all
```

Service counts in compose:

| Tier | Count | Services |
|---|---|---|
| Infrastructure | 10 | postgres, redis, nats, minio, opensearch, qdrant, temporal, temporal-ui, clamav, minio-init (one-shot) |
| Application (non-Go) | 3 | collaboration, intelligence-worker, preview-worker |
| Application (Go) | 11 | auth, policy, document, storage, search, audit, workflow, notification, signature, billing, connector |

`docker compose ps --format json | jq '[.[] | select(.Health == "healthy")] | length'`
reports ~23 healthy once every container has passed its start_period
(minio-init exits after provisioning buckets and therefore doesn't
contribute a "healthy" status).

## Development

| Command                       | Purpose                                  |
|-------------------------------|------------------------------------------|
| `make build`                  | Build all service binaries               |
| `make test`                   | Run tests with race detector             |
| `make lint`                   | golangci-lint                            |
| `make proto-gen`              | Generate Go + gateway + OpenAPI stubs    |
| `make migrate-up SERVICE=x`   | Apply migrations for service `x`         |
| `make docker-build`           | Build all service images                 |
| `make security-check`         | gosec + govulncheck                      |

See `make help` for the full list.

## Intelligence pipeline

| Stage | Trigger | Task | Persists to | Emits |
|---|---|---|---|---|
| OCR | `dms.version.uploaded.v1` | `app.tasks.ocr` | `ocr_results` | `dms.version.ocr_completed.v1` |
| Classify | `dms.version.ocr_completed.v1` | `app.tasks.classify` | `document_classifications` | `dms.classify.completed.v1` |
| NER | `dms.version.ocr_completed.v1` | `app.tasks.ner` | `entities` | `dms.ner.completed.v1` |
| Extract | `dms.version.ocr_completed.v1` | `app.tasks.extract` | `extraction_results` | `dms.extract.completed.v1` |
| Embed | `dms.version.ocr_completed.v1` | `app.tasks.embed` | Qdrant | `dms.embed.completed.v1` |
| Duplicate (sha256/minhash/simhash) | `dms.version.ocr_completed.v1` | `app.tasks.duplicate` | `duplicate_candidates`, `document_fingerprints` | — |
| Duplicate (embedding) | `dms.embed.completed.v1` | `app.tasks.dup_embedding` | `duplicate_candidates` | — |
| **Auto-tag** (ADR 0052) | `dms.classify.completed.v1` + `dms.ner.completed.v1` | `app.tasks.auto_tag` | `tag_suggestions`, `documents.tags` | `dms.autotag.completed.v1` |
| **Smart routing** (ADR 0053) | `dms.classify.completed.v1` | `app.tasks.smart_route` | `route_suggestions` | `dms.routing.completed.v1` |
| **Compliance scan** (ADR 0054) | `dms.ner.completed.v1` | `app.tasks.compliance_scan` | `compliance_findings`, `compliance_summary` | `dms.compliance.completed.v1` (+ `dms.notification.send.v1` on high+) |
| **Document Q&A** (ADR 0055) | on-demand REST | `POST /api/v1/intelligence/qa{,/sync}`, `app.tasks.rag.stream_ask` | `qa_conversations`, `qa_messages` | — (SSE response stream) |
| **Language detect** (ADR 0056) | `dms.version.ocr_completed.v1` | `app.tasks.lang_detect` | `document_languages` | `dms.language.detected.v1` |
| **Translation** (ADR 0056) | on-demand REST | `POST /api/v1/intelligence/translate`, `app.tasks.translate` | `document_translations` | `dms.translation.completed.v1` |
| **OCR quality** (ADR 0057) | `dms.version.ocr_completed.v1` | `app.tasks.ocr_quality` | `ocr_quality_scores`, `ocr_quality_summary` | `dms.ocr_quality.completed.v1` (+ `dms.version.ocr_retry_requested.v1` on auto-retry) |
| Summarize | on demand (REST) | `app.tasks.summarize` | — | `dms.summarize.completed.v1` |
| Redact | `dms.document.redacted.v1` | `app.tasks.redact` | `document_redactions` | — |

Every task: `acks_late=True`, ≤3 retries with exponential backoff + jitter, dedupe via `intel_processed_events` (tenant + consumer + event_id), DLQ on terminal failure to `dms.dlq.intel_events.<consumer>.<reason>`.

## Document-service intelligence endpoints

| Method | Path | Purpose | Auth |
|---|---|---|---|
| GET | `/api/v1/documents/{id}/duplicates` | Pending duplicate candidates | view |
| POST | `/api/v1/duplicate-candidates/{id}/confirm` | Merge: superseded → canonical | admin on retired doc |
| POST | `/api/v1/duplicate-candidates/{id}/reject` | Dismiss candidate (audit only) | view |
| GET | `/api/v1/documents/{id}/tag-suggestions` | List pending + auto-applied tags | view |
| POST | `/api/v1/documents/{id}/tag-suggestions/review` | Batch accept/reject | edit |
| GET | `/api/v1/admin/tag-suggestions` | Tenant-wide review queue (paginated) | admin/owner/compliance_officer |
| GET | `/api/v1/admin/auto-tag-config` | Per-tenant thresholds & weights | admin/owner/compliance_officer |
| PUT | `/api/v1/admin/auto-tag-config` | Patch config (partial update) | admin/owner |
| GET | `/api/v1/documents/{id}/route-suggestions` | Folder suggestions for a doc | view |
| POST | `/api/v1/documents/{id}/route-suggestions/{sid}/accept` | Move doc to suggested folder + record filing history | edit on both folders |
| POST | `/api/v1/documents/{id}/route-suggestions/{sid}/dismiss` | Reject suggestion (audit only) | view |
| GET/POST | `/api/v1/admin/routing-rules[/{id}]` | Per-tenant routing-rule CRUD | admin/owner (write), +compliance_officer (read) |
| GET/PUT | `/api/v1/admin/smart-routing-config` | Per-tenant thresholds | admin/owner (write), +compliance_officer (read) |
| GET | `/api/v1/admin/filing-analytics` | Filing patterns + suggestion acceptance rate | admin/owner/compliance_officer |
| GET | `/api/v1/documents/{id}/compliance` | PII/PHI summary + findings | view |
| POST | `/api/v1/documents/{id}/compliance/{fid}/review` | Acknowledge / remediate / mark false-positive | edit |
| GET | `/api/v1/admin/compliance/dashboard` | Tenant rollup (counts, risk dist, top entity types) | admin/owner/compliance_officer |
| GET | `/api/v1/admin/compliance/findings` | Paginated open-findings queue | admin/owner/compliance_officer |
| GET/PUT | `/api/v1/admin/compliance/config` | Per-tenant config (PHI opt-in, thresholds, custom patterns) | admin/owner (write) |
| POST | `/api/v1/admin/compliance/rescan/{id}` | Re-trigger compliance scan | admin/owner/compliance_officer |
| GET | `/api/v1/documents/{id}/ocr-quality` | Per-page scores + summary | view |
| POST | `/api/v1/documents/{id}/ocr-quality/{vid}/{page}/review` | Mark a flagged page reviewed | edit |
| GET | `/api/v1/admin/ocr-quality/review-queue` | Documents flagged for review (paginated) | admin/owner/compliance_officer |
| GET | `/api/v1/admin/ocr-quality/stats` | Tenant rollup by grade | admin/owner/compliance_officer |
| GET/PUT | `/api/v1/admin/ocr-quality/config` | Per-tenant thresholds | admin/owner (write) |

## Status

This repository is scaffolded in phases. See `docs/phases.md` for progress.

- [x] Phase A — root scaffold
- [x] Phase B — shared `pkg/` libraries
- [x] Phase C — proto contracts
- [x] Phase D — service scaffolds (build + boot; proto handlers land in Phase E)
- [x] Phase 5 — **document service reference implementation**. Template for the other 10 services.
- [x] Waves 5–14 — full blueprint structurally complete.
- [x] **Intel Feature 01 — Auto-tagging** (ADR 0052)
- [x] **Intel Feature 02 — Smart routing** (ADR 0053)
- [x] **Intel Feature 03 — Compliance heuristics (PII/PHI)** (ADR 0054)
- [x] **Intel Feature 04 — Document Q&A chat** (ADR 0055)
- [x] **Intel Feature 05 — Translation pipeline** (ADR 0056)
- [x] **Intel Feature 06 — OCR quality scoring** (ADR 0057)

## License

See [LICENSE](LICENSE).
