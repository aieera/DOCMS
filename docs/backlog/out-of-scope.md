# Out-of-Scope Ledger

Per `DMS Architecture/final.md` § 14: anything an executing agent discovers
mid-prompt that looks like a feature gap but is **not** in the State of
the Project gets recorded here and deferred. The agent **must not** build
it in the current wave.

Entry format: `| date | wave/prompt | where found | suggested fix | deferred to |`

## Pre-seeded entries (from final.md § 14.1)

| Date | Source | Item | Deferred to |
|---|---|---|---|
| 2026-04-17 | blueprint | Desktop sync client (Electron/Tauri) | post-G3 |
| 2026-04-17 | blueprint | Terraform / Ansible modules for on-prem infra | post-G2 |
| 2026-04-17 | final.md §14.1 | Collaborative live-cursor editing | declined; not in blueprint |
| 2026-04-17 | final.md §14.1 | Custom workflow authoring UI (drag-to-create) | declined; Wave 7 is read-only designer only |
| 2026-04-17 | final.md §14.1 | DocuSign-style branded envelope UI | declined; Wave 9 is PAdES-only |
| 2026-04-17 | final.md §14.1 | AI-generated document summaries auto-appended | declined; RAG is on-demand only |

## Discovered during execution

| Date | Wave/Prompt | Where found | Suggested fix | Deferred to |
|---|---|---|---|---|
| 2026-04-17 | Wave 5 / 5.1 | `RestoreVersion` emits `dms.version.restored.v1` only, not `dms.version.uploaded.v1` | Evaluate whether restored versions should re-trigger OCR/embed. Current view: no — same blob id, OCR idempotent on blob sha. | post-G1 |
| 2026-04-17 | Wave 5 / 5.1 | Grafana panel for `document_events_published_total` | Ship with Prompt 5.2's dashboard bundle | Wave 5 / 5.2 |
| 2026-04-17 | Wave 5 / 5.1 | Integration test for end-to-end outbox→NATS→consumer | Needs Prompt 5.2 stream + Wave 13.1 CI harness | Wave 13.1 |
| 2026-04-17 | Wave 5 / 5.3 | OTEL spans in intelligence service | Tracer not configured service-wide; Surya/Paddle calls are sync; span-per-page would need per-thread tracer | Wave 13.6 (observability bundle) |
| 2026-04-17 | Wave 5 / 5.3 | Per-page 90s OCR timeout (spec wanted it, task uses 30-min total instead) | Needs a watchdog on the synchronous Surya calls | Wave 13.3 (chaos) if customer docs hit the 30m cap |
| 2026-04-17 | Wave 5 / 5.3 | `gc_processed_events` cron for ocr_processed_events | Manual SQL today | Wave 8.1 (Temporal retention cron) |
| 2026-04-17 | Wave 5 / 5.3 | DLQ auto-replay job | Manual `dms-admin nats replay` only | post-G1 |
| 2026-04-17 | Wave 5 / 5.4 | `categories` taxonomy table + FK | Migration uses `category_key TEXT` slug; FK deferred until a product need exists | post-G1 |
| 2026-04-17 | Wave 5 / 5.4 | Per-tenant cap for classify / NER consumers | Only OCR has it; classify + NER are cheap | post-G1 |
| 2026-04-17 | Wave 5 / 5.4 | OTEL tracer config for intelligence service | Required for DoD § 1.4 #8; pulled out of individual prompts | Wave 13.6 |
| 2026-04-17 | Wave 5 / 5.5 | `page_number` in Qdrant payload | Needs OCR to pass page maps forward; start_char/end_char suffice for highlighting | post-G1 |
| 2026-04-17 | Wave 5 / 5.5 | `classification_top_1` in Qdrant payload | Requires DB lookup per chunk upsert; search can join on classify event | post-G1 |
| 2026-04-17 | Wave 5 / 5.5 | Per-tenant isolated Qdrant collections (`isolated-vector-store` opt-in) | Single-collection + payload filter handles pilot | post-G2 |
| 2026-04-17 | Wave 5 / 5.5 | Async embedding concurrency cap of 4 | Embedder is sync today; constant is advisory | post-G1 |
| 2026-04-17 | Wave 6 / 6.1 | `dms-admin kms rewrap --tenant <id>` online re-wrap of historical DEKs | Rotation retires alias but keeps it resolvable; post-compromise re-wrap needs streaming copy | Wave 6 follow-up |
| 2026-04-17 | Wave 6 / 6.1 | Auto-allocate v1 KEK at tenant creation | Auth service Register path doesn't call `kms create`; run manually until Wave 12.3 | Wave 12.3 |
| 2026-04-17 | Wave 6 / 6.1 | 24h CMK scheduled deletion for de-provisioned tenants | Needs control-plane workflow + Vault/AWS CMK lifecycle | Wave 12.3 |
| 2026-04-17 | Wave 6 / 6.1 | VaultKeyManager + AWSKMSKeyManager implementations | Stubs only; local HKDF is dev/on-prem default | Wave 6 follow-up |
| 2026-04-17 | Wave 6 / 6.1 | `kms_derive_total`, `kms_decrypt_errors_total` Prom metrics | LocalKeyManager doesn't yet expose a prometheus registry | Wave 13.6 |
| 2026-04-17 | Wave 6 / 6.2 | Refresh-token cookie `dms_refresh` on `/auth` path | Needs new schema + rotation endpoint; one-shot 24h sessions cover pilot | post-G1 |
| 2026-04-17 | Wave 6 / 6.2 | CSRF middleware on document/storage/search/billing services | Only auth wrapped today; frontend sets header on all mutations so exploitability low | post-G1 |
| 2026-04-17 | Wave 6 / 6.2 | Bearer-session deprecation warning log | Would flood current client logs; turn on when 30-day window starts | post-G1 |
| 2026-04-17 | Wave 6 / 6.2 | `csrf_rejections_total{reason}` Prom counter | Middleware currently writes 403 without emitting a labelled metric | Wave 13.6 |
| 2026-04-17 | Wave 7 / 7.4 | Workflow service queries skip `SET LOCAL app.current_tenant` (RLS enforced only via WHERE clause) | Wrap `repository.Repository` reads + `activities.Activities` direct-pool writes in `WithTenantTx` / session-scoped tenant setting | Wave 11 (RLS audit) |
| 2026-04-17 | Wave 7 / 7.4 | Tasks page missing Delegate action | Needs user-picker component | Wave 10 |
| 2026-04-17 | Wave 7 / 7.4 | Task-detail drawer (full instance timeline) | New component work | Wave 10 |
| 2026-04-17 | Wave 7 / 7.4 | Real-time inbox updates | SSE / NATS bridge; polling covers pilot | post-G1 |
| 2026-04-17 | Wave 7 / 7.5 | Auto-layout (dagre/elkjs) for large workflow DAGs | Hand-tuned coords are fine for ≤20-node definitions; swap when customer exceeds | post-G1 |
| 2026-04-17 | Wave 7 / 7.5 | Live instance overlay on designer (highlight active step) | Needs instance-state lookup + per-step status color; folds into task detail drawer | Wave 10 |
| 2026-04-17 | Wave 7 / 7.5 | Playwright visual-regression for workflow graph | E2E harness ships later | Wave 13.1 |
| 2026-04-17 | Wave 8 / 8.1 | Two-person dispose-approval workflow (archived → disposed + ciphertext shred) | Interactive workflow separate from retention cron; cron emits `dispose_candidate.v1` only | Wave 8 follow-up |
| 2026-04-17 | Wave 8 / 8.1 | Retention-policy rules engine populating `documents.retention_until` on create | `retention_policies` table exists but isn't consulted at document create time | Wave 8 follow-up |
| 2026-04-17 | Wave 8 / 8.1 | Admin UI for retention-policy CRUD | No endpoints yet; policies edited via direct SQL | Wave 10 |
| 2026-04-17 | Wave 8 / 8.1 | `retention_*_total` Prom counters | Activity instrumentation needed | Wave 13.6 |
| 2026-04-17 | Wave 8 / 8.2 | OPA `role=compliance_officer` gate on hold create/release | Handler reads headers only; needs policy.Check call or middleware | Wave 11 |
| 2026-04-17 | Wave 8 / 8.2 | Redaction endpoint + hold gate on it | Endpoint doesn't exist yet | Wave 12 |
| 2026-04-17 | Wave 8 / 8.2 | Proper create-hold UI (doc multi-select) + release modal | Placeholder uses window.prompt today | Wave 10 |
| 2026-04-17 | Wave 8 / 8.2 | Remove dead code in compliance/retention.go (CreateLegalHold/ReleaseLegalHold/RetentionEnforcer) | Unreferenced by boot | Wave 11 |
| 2026-04-17 | Wave 8 / 8.2 | Hold-extension / expiry column | Schema has no expiry; spec's "extend" clause is vacuous until added | Wave 8 follow-up |
| 2026-04-17 | Wave 8 / 8.2 | `legal_holds_applied_total` / `legal_holds_released_total` Prom counters | Wave 13.6 |
| 2026-04-17 | Wave 8 / 8.3 | ZIP packaging + signed-URL export for DSR | Needs DSR bucket + storage-service presigned-upload path; workflow returns counts only today | Wave 11 (storage integration) |
| 2026-04-17 | Wave 8 / 8.3 | Cross-service erase activities (qdrant, notifications, search) | Each service registers its own activity with workflow | Wave 11 |
| 2026-04-17 | Wave 8 / 8.3 | DSR verification-token email round-trip | Honor-system today (ADR 0024 §6); needs notification service transactional email | Wave 11 |
| 2026-04-17 | Wave 8 / 8.3 | `privacy_ledger` 7-year retention sweep | Operator SoP today; cron lands later | Wave 12 |
| 2026-04-17 | Wave 8 / 8.3 | "DSR running > 25 days" alert (approaching 30-day SLA) | Prom alert + dashboard | Wave 13.6 |
| 2026-04-17 | Wave 8 / 8.3 | `dsr_requests_total{type,status}` / `dsr_hold_blocks_total` Prom counters | Wave 13.6 |
| 2026-04-17 | Wave 8 / 8.4 | Physical blob move + re-encrypt under region-local KEK | Needs storage-service cross-region copy activity; today workflow flips `region_pin` + emits event only | Wave 12 |
| 2026-04-17 | Wave 8 / 8.4 | Per-tenant default region override admin endpoint | `organizations.region_pin` exists but no CRUD; control-plane work | Wave 12.3 |
| 2026-04-17 | Wave 8 / 8.4 | Cancel / pause running migration from admin UI | `temporal workflow cancel` works today | post-G1 |
| 2026-04-17 | Wave 8 / 8.4 | Multi-region KEK aliases (separate master per region) | Wave 6.1 single-master derivation; prod needs Vault/AWS KMS per region | Wave 12 |
| 2026-04-17 | Wave 8 / 8.4 | `residency_migrations_total{target}` / `residency_doc_moves_total` Prom counters | Wave 13.6 |
| 2026-04-17 | Wave 9 / 9.1 | DSS-sidecar performance benchmark validation | ADR claims 8-10 PAdES-B-LT/sec/core; needs harness | Wave 13.3 |
| 2026-04-17 | Wave 9 / 9.1 | Threat model for Go↔Java sidecar boundary | Secrets per-request, not at rest; formal TM later | Wave 13.4 |
| 2026-04-17 | Wave 9 / 9.2 | Java DSS sidecar (Gradle project, Dockerfile, gRPC Sign/Verify) | Follow-up prompt; signer interface + mock land now | Wave 9.2b |
| 2026-04-17 | Wave 9 / 9.2 | Temporal workflow wiring to signer.Signer | SignatureWorkflow still the Wave 7.1 stub | Wave 9.2b |
| 2026-04-17 | Wave 9 / 9.2 | /signatures/envelopes route aliases with fields[] body | Existing /requests shape covers the same data | Wave 9.2b |
| 2026-04-17 | Wave 9 / 9.2 | Admin in-flight envelopes page | Reuses existing APIs | Wave 9.2b |
| 2026-04-17 | Wave 9 / 9.2 | Adobe Reader validation harness in CI (pdf-signature-validator) | DoD depends on real sidecar | Wave 9.2b |
| 2026-04-17 | Wave 9 / 9.2 | Vault / AWS KMS / PKCS#11 signer backends | LocalKeyManager works in dev; prod backends need adapters | Wave 12 |
| 2026-04-17 | Wave 9 / 9.2 | `signature_*` Prom counters (envelopes/sign duration/TSA errors/KMS errors/LTV missing) | Runbook documents planned metrics | Wave 13.6 |
| 2026-04-17 | Wave 10 | User-picker combo for group member add (currently raw UUID input) | UI polish | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Bulk add/remove group members | Easy follow-up | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Go handler-level tests for /api/v1/admin/groups | Package lacks test harness | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Tag rename | Needs `documents.tags` array scrub across docs | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Tag bulk delete / color edit | Follow-up | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Custom-hex color input on Tags page (currently 12 presets) | UI polish | Wave 10 follow-up |
| 2026-04-18 | Wave 10 | Live rego introspection for permission matrix | Curated Go mirror today; CI drift guard (`scripts/check-policy-matrix-sync.sh`) planned | Wave 11 |
| 2026-04-18 | Wave 10 | Per-tenant permission matrix overrides | Global matrix today; needs tenant-scoped layer when custom rego ships | Wave 11 |
| 2026-04-18 | Wave 10 | "Why can Alice edit X?" explain endpoint | OPA trace mode exists but no HTTP surface | Wave 11 |
| 2026-04-17 | Wave 10 | SSO wizard (SAML/OIDC metadata upload + test assertion) | Multi-step flow | Wave 10 follow-up |
| 2026-04-18 | Wave 10 | Retention policies: last-accessed / custom trigger | Schema uses created_at only | Wave 10 follow-up |
| 2026-04-18 | Wave 10 | Retention policies: "which docs match" preview | Needs filter-evaluation endpoint | Wave 10 follow-up |
| 2026-04-18 | Wave 10 | `retention_policies_*_total` Prom counters | Wave 13.6 |
| 2026-04-17 | Wave 10 | Metadata schema editor (JSON Schema, monaco) | UI work | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Tags admin (tenant-level tag CRUD) | Per-doc tagging exists | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | `dms.sharelink.revoked.v1` outbox event on individual + bulk revoke | Audit relies on `audit_events` today via OPA requirePermission | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Share-link filter (title/creator/expires-before) + CSV export | UI polish | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | `share_links_revoked_total` / `share_links_revoke_all_total` Prom counters | Wave 13.6 |
| 2026-04-17 | Wave 10 | Share-link admin (list/revoke/revoke-all per doc) | Share-link API exists | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | Webhook pause/resume toggle, delivery-detail modal | Follow-up to webhooks page | Wave 10 follow-up |
| 2026-04-17 | Wave 10 | `webhook_rotations_total` / `webhook_redeliveries_total` Prom counters | Wave 13.6 |

## Rules

1. If a prompt's scope balloons past its defined deliverables, stop and
   log here rather than growing the PR.
2. If a feature is needed to make another feature work, log it but still
   finish the current prompt with a stub; do not re-scope.
3. Re-check this ledger at the start of every wave to see if anything
   deferred is now in scope.
