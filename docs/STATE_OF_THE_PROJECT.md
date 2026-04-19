# VaultDMS — State of the Project

**Baseline:** 2026-04-17 (post Waves 1–4 of audit remediation).
**Companion doc:** `DMS Architecture/final.md` — the Enterprise Completion
Prompt that drives Waves 5–14. Read this doc for *where we are*; read
final.md for *where we're going*.

## Service truth table

| Service | Status | Reality on disk |
|---|---|---|
| auth | ✅ Live | password, TOTP, SAML/OIDC, SCIM, API keys, `/auth/me` (Wave 1). **Defect:** `math/rand` serial in SAML signer. |
| policy | ✅ Live | OPA embedded + Redis cache. Emits `dms.user.*` with **no matching JetStream** (silent loss). |
| document | ✅ Live | CRUD + versioning + workspace CRUD (Wave 3) + `RestoreVersion` (Wave 2a) + storage REST proxy (Wave 2b). |
| storage | 🟡 **blocker** | gRPC OK. Upload completes but **never publishes `dms.version.uploaded.v1`** → pipeline halts. Shared KEK. |
| search | ✅ Live | OpenSearch indexer, hybrid skeleton, saved-searches path fixed (Wave 1), saved-searches UI (Wave 4). |
| intelligence | 🟡 | Chunk + embed code present. **Qdrant upsert stubbed.** RAG endpoint reachable, not integration-tested. |
| preview | 🟡 | Python service boots; NATS consumer wired. Frontend viewer still uses raw PDF, not rendered pages. |
| workflow | ❌ | Temporal connection code only. **Zero workflow definitions.** "My Tasks" has nothing to surface. |
| notification | ✅ | Rate-limited email + web push working. |
| audit | 🟡 | Hash chain + GET events + CSV export (Wave 4). **`verify-integrity` endpoint not exposed.** |
| signature | ❌ | Skeleton only. **No PAdES library integrated.** |
| collaboration | 🟡 | Node service boots. Auth middleware + comment event handlers incomplete. |
| connector | 🟡 | Webhook delivery done. M365/SFDC OAuth scaffolded, not connected. |
| billing | 🟡 | Stripe webhooks skeleton. `usage_records` cron errors every minute (known residual). |

## The critical blocker

**`dms.version.uploaded.v1` has no publisher.** Fixing this in
`services/storage` `CompleteUpload` is Wave 5 Prompt 5.1 and unblocks
prompts 9, 10, 11, 12, 14, 16 from the original 30-prompt plan.

## Silent data-loss condition

NATS streams only cover `dms.document.>` + `dms.version.>`. Every
`dms.user.*`, `dms.policy.*`, `dms.billing.*` publish is accepted by the
broker and routed nowhere. User creations, role changes, and API-key
rotations silently vanish from the event bus. Fix: Wave 5 Prompt 5.2.

## Security debt blocking pilot

1. Shared KEK `vaultdms-storage-default` across all tenants — per-tenant KEK needed (Wave 6.1).
2. Session token in `localStorage` — httpOnly cookie + CSRF needed (Wave 6.2).
3. `math/rand` for X.509 serial in SAML signer (Wave 6.3).
4. 14 NATS handlers use `context.Background()` (Wave 6.4).
5. 4 services publish direct to NATS instead of outbox (Wave 6.5).

## What Waves 1–4 shipped (reference)

- **Wave 1 (11a):** 9 typo/contract fixes + `/auth/me` handler + dead-code cleanup.
- **Wave 2a (11b):** share-link dialog wired, restore-version RPC end-to-end.
- **Wave 2b (11c):** storage REST proxy on document service (option B, preserves trust boundary).
- **Wave 3 (11d):** workspace CRUD ground-up + folder rename/delete + document move frontend wiring.
- **Wave 4 (11e):** MFA / sessions / API keys UI, audit CSV export, saved-searches UI.

Remediation docs at `docs/audit/remediation/11a-e.md`.

## Backend compile state

`go build ./...` across all 15 `go.work` modules succeeds (verified
2026-04-17). `cd proto && buf generate` has been run; all generated
stubs are current.

## Running on this machine

Compose: postgres (15432), redis (6379), nats (4222), minio (9000),
opensearch (9200), plus `temporal`, `qdrant`, `clamav`. Services up:
auth:8081, policy:8082, billing:8083. Vite on :3000. Credentials:
tenant `acme`, email `admin@acme.local`, password `ChangeMe!Now2026`.

## Completion map

`~60%` of the original 30-prompt plan is shipped-or-partial. `~20%`
plumbed-but-disconnected (intelligence pipeline). `~20%` not started
(workflow engine, signatures, compliance cron, control plane).

**Gate targets per final.md § 1.2:**
- G1 pilot-ready: ~3 weeks / 4 engineers after Wave 5 starts.
- G2 production SaaS: ~2–3 months / 6 engineers.
- G3 feature-complete: ~6 months.
