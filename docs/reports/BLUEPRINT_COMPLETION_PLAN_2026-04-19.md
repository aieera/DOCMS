# Blueprint-Aligned Completion Plan

**Generated:** 2026-04-19
**Objective:** Reset the project onto the blueprint ([`DMS Architecture/dms-blueprint.md`](../../DMS%20Architecture/dms-blueprint.md)) as the *sole* source of truth, and complete it to **enterprise-grade** quality across all 22 blueprint sections.

**Rule for every decision below:** if a change isn't traceable to a blueprint section, it doesn't ship. Additions accumulated outside the blueprint are listed in §2 with explicit keep/retire calls.

---

## 0. Guiding principles (non-negotiable)

1. **Blueprint-first.** Every user story and engineering ticket must cite the blueprint § it fulfills. Work without a citation is rejected at code review.
2. **Enterprise = invariants, not features.** Features alone don't clear the enterprise bar; enterprise is (a) security-audited, (b) compliance-evidenced, (c) observable, (d) documented, (e) DR-tested.
3. **No new abstractions, languages, or libraries outside the blueprint stack** without a written exception citing a blueprint constraint that can't be satisfied otherwise.
4. **All blueprint §22 anti-patterns are enforced in CI** (lint rules + architecture tests).
5. **Exit criteria per quarter match blueprint §21 exactly** — no soft landings.

---

## 1. Current position against blueprint §21 roadmap

Blueprint §21 defines an 8-quarter (24-month) plan. Mapping reality:

| Quarter | Blueprint exit criteria | Reality today | Verdict |
|---|---|---|---|
| **Q1 — Foundation** | "Can create a tenant, log in, upload a doc, view it in a folder, download it. Basic RBAC works." | Tenant ✅, login ✅, RBAC ✅, folder view ✅, **upload's event pipeline broken (storage never publishes `dms.version.uploaded.v1`)**, **API gateway absent** | 🟡 **90% complete — two hard gaps** |
| **Q2 — Intelligence + Search** | "Documents are OCR'd, searchable, previewable. Semantic search returns relevant results." | Lexical search ✅, semantic ❌, OCR code exists but pipeline never triggered (depends on Q1 gap), preview service not default-started | 🟡 **30% complete — gated by Q1 blocker** |
| **Q3 — Signatures + Workflows + Collaboration** | "Run a 3-step approval workflow, sign a document, co-edit a DOCX, receive notifications." | Workflow: Temporal up, **zero definitions, no workflow can run**. Signatures: REST skeleton, no PAdES lib, **cannot complete a signature**. OnlyOffice/co-edit: ⬜. Notifications ✅. | ❌ **~15% complete** |
| **Q4 — On-Prem + Observability** | "Customer can deploy on-prem via Helm, with monitoring, air-gapped." | Helm chart (91 templates) exists, **untested on real cluster**. Monitoring scaffold exists, dashboards/alerts unverified. Air-gap scripts + values file present, unverified. | 🟡 **~40% complete** |
| **Q5 — SSO/SCIM + Compliance** | "SSO works with Okta/Azure AD. SCIM provisions. Legal holds prevent deletion. GDPR export/erasure works." | SAML code ships with `math/rand` defect (blocks prod); OIDC ✅ in code; SCIM endpoints ✅; legal-hold schema+RPC ✅; GDPR REST routes ✅ but hash-chain verify endpoint not exposed. **None IdP-verified.** | 🟡 **~50% complete — unverified** |
| **Q6 — SOC 2 + Security Hardening** | "Pass SOC 2 Type II readiness. Bug bounty live. DLP blocks PII in shared links." | DLP pkg stub. No SOC 2 evidence automation. No pen test. No bug bounty. | ❌ **<5% complete** |
| **Q7 — Vertical Connectors + Mobile** | "Core connectors live. Mobile in app stores. Desktop client installable." | Connector OAuth scaffolded (Salesforce/M365/Google). Mobile dir exists, build unverified. Desktop Tauri ⬜. Browser extension ⬜. | ❌ **~10% complete** |
| **Q8 — Platform + Ecosystem** | "Public API stable + documented. Marketplace. Load-tested to 100K users / 100M docs." | OpenAPI spec drafted, currency unverified. Webhooks ✅. MCP SSE ✅. No marketplace. No load-test at target scale. | ❌ **~15% complete** |

**Net position:** effectively **late Q1 / early Q2** in feature-completeness, with Q4-Q5 partially plumbed ahead of turn. Gaps are quality (enterprise invariants) more than feature count.

---

## 2. Things added beyond blueprint — keep/retire decisions

| Addition | Blueprint relation | Decision | Why |
|---|---|---|---|
| `signature-signer` Java sidecar ([services/signature-signer/](../../services/signature-signer/)) | Blueprint §11 has a monolithic Go signature service | **KEEP** | PAdES-B-LT signing via DSS (Digital Signature Service) is Java-native; replicating DSS in Go is ~3 person-years. The sidecar is a sound refinement of §11, enforced by mTLS. Update blueprint §11.2 to document the split. |
| `dms-admin` CLI (`nats bootstrap/list/replay/check`) ([cmd/dms-admin/](../../cmd/dms-admin/)) | Not in blueprint | **KEEP** | Ops tooling. Blueprint §15.5–15.8 (Ops) is thin; this fills a gap. Add "Operational CLI" to §15. |
| `scripts/preflight.sh` NATS subject coverage check | Not in blueprint | **KEEP** | Prevents an entire class of silent production bugs. Belongs in §22 anti-patterns (never publish to an uncovered subject) + CI gate. |
| Billing local `/readyz` with honest DB probe | Blueprint §15.1 implies deep health checks | **KEEP** and propagate | Extend `pkg/health` with the same pattern (each service probes its own critical DB/resource). |
| `LEGACY_EVENTS` NATS stream for pre-Wave-5 subjects | Not in blueprint | **RETIRE after migration** | Migration aid only. Once all publishers emit new-topology subjects, drop `LEGACY_EVENTS` per §4.7. |
| Billing's own [services/billing/migrations/000010_reconcile_billing_schema](../../services/billing/migrations/) + separate `billing_schema_migrations` table | Blueprint assumes each service owns its migrations | **KEEP**; extend to every service | Expose as a pattern: *each service must own its own migrations table*. See §4 of this plan. |
| Vite dev proxy fan-out ([web/vite.config.ts](../../web/vite.config.ts)) | Blueprint §3.1 calls for Kong/Envoy gateway | **TEMPORARY — replace with real gateway** | Dev-time only. The **missing API gateway** is the single biggest architectural gap. Vite proxy is a workaround, not a home. |
| Per-service host HTTP ports 8180-series in [scripts/run-all-services.sh](../../scripts/run-all-services.sh) | Dev convenience only | **KEEP for dev**; production path is Kubernetes + gateway | |

**Nothing currently added is in conflict with the blueprint.** The additions are ops/dev-loop refinements. The real blueprint deviations are **gaps** (missing gateway, broken pipeline, empty workflow, no PAdES), not additions.

---

## 3. Completion plan — by blueprint dependency, not by quarter

Blueprint §21 is calendar-sliced; reality needs a **dependency-sliced** reset. This section re-orders work by what unblocks what.

### Phase A — Unblock the core event pipeline (1–2 weeks)

Blocking all of §6 (Intelligence), §7 (Search — semantic), §10 (Workflows triggered on doc events).

| Task | § | Deliverable | Acceptance |
|---|---|---|---|
| A1. Storage service: publish `dms.version.uploaded.v1` in `CompleteUpload` via outbox | 4.7, 5.1, 6.1 | PR to [services/storage/](../../services/storage/) | Upload a doc via REST; observe event land in DOC_EVENTS; intelligence worker (once running) picks it up; OCR result row created. |
| A2. Start intelligence + preview + collaboration services by default (move out of `app` compose profile or promote) | 3.2, 6.x | `docker-compose.yml` edit | `docker compose up` brings all 14 services (not just 11 Go ones). Health checks ✅ for each. |
| A3. Ensure `dms.version.uploaded.v1` is covered by DOC_EVENTS (or create a dedicated stream) | 4.7 | `pkg/events/publisher.go` | `dms-admin nats check dms.version.uploaded.v1` returns OK. |
| A4. Fix schema drift in audit (`actor` column) and connector (`webhook_subscriptions.active` column), analogous to billing's W10 migration | 4.1 | Per-service migration files | Each service's handler hits its table without column errors; `/api/v1/audit/events` → 200. |
| A5. Apply search service's pending migration against the shared schema (or migrate search to its own table like billing) | 4.1 | Migration table convention | `saved_searches` exists; `/api/v1/saved-searches` → 200. |

**Exit:** a user uploads a document, the backend emits `dms.version.uploaded.v1`, OCR fires, results are indexed in OpenSearch, the document appears in search results. End-to-end pipeline green.

### Phase B — Introduce the API gateway (2 weeks)

Blocks all of §14.3 (Rate Limiting), §20 (Threat Model — gateway is every threat's mitigation), and closes the "handler trusts raw `X-Tenant-ID` header" vulnerability in 5 services.

| Task | § | Deliverable | Acceptance |
|---|---|---|---|
| B1. Deploy Kong (or Envoy — blueprint says either) in the Helm chart and docker-compose | 3.1, 14.3 | `deploy/helm/vaultdms/templates/gateway/` + compose service | Gateway container healthy; all ingress goes through it. |
| B2. Route all public REST through the gateway: terminate TLS, auth, rate-limit, correlation-id, route to backend services | 3.1 | Gateway config | Audit/notification/workflow/signature/search no longer directly exposed. Handler-level session validation added as defense in depth. |
| B3. Replace the Vite dev proxy with a single target pointing at the gateway | — | [web/vite.config.ts](../../web/vite.config.ts) | One-line proxy entry again: `/api → localhost:<gateway-port>`. |
| B4. Rate-limit policies per [§14.3 table 1](../../DMS%20Architecture/dms-blueprint.md) (auth, doc ops, search, upload) | 14.3 | Gateway policies | k6 load test: 429s at documented thresholds. |
| B5. Move `/api/v1/auth/mfa/verify` et al. to gateway-level rate limits per §8.1 | 8.1, 14.3 | | Brute-force test blocked after N attempts per IP. |

**Exit:** all public traffic enters through the gateway; handler-level `X-Tenant-ID` trust is validated against the session; rate limits are enforced at the edge per blueprint §14.3.

### Phase C — Close the Q1 foundation gaps properly (2 weeks)

| Task | § | Deliverable | Acceptance |
|---|---|---|---|
| C1. Per-tenant KEK rotation (replace shared KEK) | 5.8, 8.5 | `pkg/crypto` + storage service | New tenant gets a distinct KEK; rotate test passes; re-wrap of DEKs works. |
| C2. Convert auth session storage from `localStorage` to cookie-only end-to-end (cookie path already added; remove any remaining localStorage references) | 8.1, 17.1 | Frontend audit | No `localStorage` access for session token; OWASP scan clean. |
| C3. `crypto/rand` for SAML X.509 serial | 8.1 | `services/auth/internal/sso/saml.go` | Static analysis flag cleared. |
| C4. All NATS handlers receive a request-scoped context (not `context.Background()`) | 8.8, 15.3 | 14 handlers across services | Tracing shows parent span; correlation-id propagates. |
| C5. Every service publishing events uses the outbox (no direct `nats.Publish`) | 4.7 | 4 services flagged in [STATE_OF_THE_PROJECT.md:46](../STATE_OF_THE_PROJECT.md#L46) | Architecture test (`go test ./pkg/archtest`) fails on any direct NATS publish. |
| C6. Per-service migration tables (like billing's `billing_schema_migrations`) for all 11 services | 4.1 | Migration scripts + CI check | Each service can `make migrate-up SERVICE=x` independently without touching another service's version. |

**Exit:** the security debt enumerated in [docs/STATE_OF_THE_PROJECT.md:40-46](../STATE_OF_THE_PROJECT.md#L40-L46) is fully closed. **Clears blueprint's "pilot-ready" gate (G1).**

### Phase D — Q2 Intelligence to done (3–4 weeks)

Pre-reqs: Phases A+B complete.

| Task | § | Deliverable |
|---|---|---|
| D1. OCR quality bar: Arabic + English + mixed docs per §21 Q2 Arabic-corpus risk note | 6.1 | Benchmark harness in `tests/intelligence/` |
| D2. Structured extraction: vendor schemas (W-9, passport, invoice) + confidence scores | 6.2 | `extraction_results` with Pydantic schemas |
| D3. Classification via distilbert — wired, evaluated, stored | 6.3 | `classifications` table populated per version |
| D4. NER (spaCy) + entity tables populated | 6.6 | `entities` table |
| D5. **Qdrant upsert fully implemented** (currently stubbed) | 6.8 | `document_chunks` → Qdrant vectors |
| D6. Semantic search end-to-end: hybrid scoring (BM25 + cosine) in search service | 7.1 | `/api/v1/search` returns hybrid ranked results |
| D7. Vendor-neutral LLM routing — OpenAI + Anthropic + Bedrock + local tested | 6.9 | Provider contract test per blueprint table |
| D8. RAG Q&A endpoint end-to-end | 6.8 | `/api/v1/intelligence/ask` returns cited-passage answer |
| D9. Preview service rendering: DOCX/PPTX/XLSX → PDF via LibreOffice headless | 5.4 | Preview appears in viewer |
| D10. PDF.js annotation layer | 17.3 | Comments attach to coords; persist across sessions |

**Exit:** Q2 blueprint exit criteria met verbatim.

### Phase E — Q3 Workflows / Signatures / Collab (4 weeks)

| Task | § | Deliverable |
|---|---|---|
| E1. Workflow Designer (ReactFlow) — drag-drop definition authoring | 10.1, 17.x | `workflow_definitions` populated via UI |
| E2. 3-step approval workflow end-to-end | 10.2 | Definition → instance → 3 signals → completion → notification |
| E3. Integrate DSS in `signature-signer` sidecar; implement PAdES-B-LT signing + TSA round-trip | 11.1, 11.3 | `Sign` RPC works; verifies in Adobe Reader |
| E4. LTV (Long-Term Validation) for PAdES signatures | 11.3 | VRI dict populated; signature validates 10+ years |
| E5. DocuSign + Adobe Sign adapters (route per tenant setting) | 11.2 | Conformance test per provider |
| E6. OnlyOffice integration for DOCX co-editing (embedded viewer) | 10.3, 17.3 | Open a DOCX → 2 users co-edit; changes propagate |
| E7. Yjs CRDT layer for comments + presence | 17.4 | Real-time comment + cursor sync |
| E8. Notification channels: email (SES/SendGrid), push (FCM/APNs), Slack, Teams | 10.8 | Per-channel delivery test |

**Exit:** Q3 exit criteria verbatim.

### Phase F — Q4 Observability + On-Prem (3 weeks)

| Task | § | Deliverable |
|---|---|---|
| F1. Helm chart installed on a real Kubernetes cluster (kind/minikube in CI + one real cloud) | 13.1 | `helm install` + smoke test in CI |
| F2. Air-gapped install from tarball (scripts/airgap) | 13.6 | Installable on a disconnected VM |
| F3. Grafana dashboards for top-20 SLIs per [§15.2](../../DMS%20Architecture/dms-blueprint.md) | 15.2 | 20 dashboards provisioned |
| F4. Prometheus Alertmanager rules per [§15.4](../../DMS%20Architecture/dms-blueprint.md) — 4-tier | 15.4 | Alert fires in chaos tests |
| F5. OpenTelemetry end-to-end: traces span gateway → service → DB; exporter configured | 15.3 | One trace from a browser click to a DB query |
| F6. License enforcement for on-prem | 13.x | Expired license blocks writes; warns for N days prior |

**Exit:** Q4 exit criteria verbatim.

### Phase G — Q5 SSO/SCIM + Compliance (3 weeks)

| Task | § |
|---|---|
| G1. SAML 2.0 end-to-end with Okta + Azure AD test tenants | 8.1 |
| G2. OIDC with the same | 8.1 |
| G3. LDAP/AD direct integration for on-prem | 13.5 |
| G4. SCIM 2.0 conformance suite (Okta conformance test) | 8.3 |
| G5. Retention policies engine: schedule deletion, enforce | 9.4 |
| G6. Legal-hold blocks delete at DB and blob layers (not just UI) | 9.3 |
| G7. GDPR Art. 15/16/17/20 automation end-to-end | 9.2 |
| G8. Audit hash-chain verify endpoint exposed + scheduled daily verification | 8.8 |
| G9. Chain-of-custody eDiscovery export (signed ZIP) | 9.5 |

**Exit:** Q5 exit criteria verbatim.

### Phase H — Q6 SOC 2 + Security Hardening (2 months, plus 3-month audit window)

| Task | § | Deliverable |
|---|---|---|
| H1. SOC 2 Trust Services Criteria (CC1-CC9) evidence automation: nightly job produces evidence bundles | 9.6 | Evidence bundle uploaded to cold storage |
| H2. External penetration test — hire firm | 20.x | Report; remediations tracked |
| H3. DLP pipeline: Presidio or custom regex/ML for PII detection on upload + shared link | 8.7 | PII found → tagged/blocked per tenant policy |
| H4. BYOK / HYOK: per-tenant KMS endpoint; rotate keys without downtime | 5.8 | Rotate test; encryption per-tenant key |
| H5. Bug bounty program (HackerOne / Bugcrowd) | 20.x | Program live; first report triaged in <72h |
| H6. Threat model per [§20.1 STRIDE table](../../DMS%20Architecture/dms-blueprint.md) — one doc per service | 20.1 | 11 documents in `docs/security/stride/` |

**Exit:** SOC 2 Type II readiness confirmed by assessor. Bug bounty live. Pen test findings remediated.

### Phase I — Q7 Mobile + Connectors (4 weeks)

| Task | § |
|---|---|
| I1. React Native app: login + doc browse + upload + scan-to-OCR + offline cache | 17.5 |
| I2. Salesforce connector: OAuth + sync + bi-directional link | 12.4 |
| I3. SAP connector | 12.4 |
| I4. Microsoft 365 connector + email ingestion | 12.4 |
| I5. Tauri desktop client: virtual drive + selective sync + conflict UI | 17.6 |

**Exit:** apps on app stores; connectors live for 3+ real customer tenants.

### Phase J — Q8 Platform + Ecosystem (4 weeks)

| Task | § |
|---|---|
| J1. Public REST API v1.0 frozen + SemVer + `Sunset` header policy | 12.1 |
| J2. OpenAPI spec auto-generated from code, published to `docs/api/` | 12.1 |
| J3. SDK codegen for Python + TypeScript + Go | 12.1 |
| J4. Zapier + Make integration | 12.5 |
| J5. Connector SDK — docs + scaffold | 12.4 |
| J6. Browser extension (Chrome/Firefox/Edge) | 17.7 |
| J7. Load test at blueprint scale targets: 100K users, 100M docs | 16.7 |

**Exit:** Q8 exit criteria verbatim.

---

## 4. Enterprise-level invariants (run continuously, never complete)

These are not features; they're the **quality bar** an enterprise buyer evaluates.

| Invariant | Evidence expected |
|---|---|
| **Availability SLO: 99.95%** per [§15.4](../../DMS%20Architecture/dms-blueprint.md#L1396) | Public status page with 90-day history |
| **p99 API latency < 500 ms** per [§16.7](../../DMS%20Architecture/dms-blueprint.md#L1472) | Grafana dashboard + monthly report |
| **Security incident response: 15 min triage, 4 hr remediation for P0** | IR runbook + on-call rotations + post-mortem library |
| **Every PR runs: lint + unit + integration + architecture-test + SAST (gosec + govulncheck + semgrep)** | CI required checks list |
| **Every release signed (Sigstore/cosign), SBOM published (CycloneDX)** | Release artifacts |
| **DR drill: full region failover quarterly** | Drill log + RTO/RPO actuals vs targets (RTO ≤ 4 hr, RPO ≤ 15 min) |
| **Audit log integrity verified daily** per [§8.8](../../DMS%20Architecture/dms-blueprint.md#L992) | Cron job + alert on chain break |
| **Residency violations: 0 (blueprint-hard)** per [§9.1](../../DMS%20Architecture/dms-blueprint.md#L1033) | Violations-found metric on dashboard |
| **Test coverage: ≥70% Go, ≥60% frontend** | CI gate |
| **Zero Critical/High SAST findings at merge** | CI gate |
| **Dependency freshness: no CVE > 30 days unpatched** | Renovate/Dependabot SLA |
| **Backups tested quarterly: restore to staging from each region** | Drill log |

---

## 5. Exit criteria per phase (enterprise bar)

A phase is only "done" when all of:

- ✅ All blueprint §21 exit criteria for the relevant quarter pass on a real environment (not localhost).
- ✅ Code is merged, signed, and deployed to staging.
- ✅ Test coverage thresholds hit for touched code.
- ✅ Runbook written for the feature ([`docs/runbooks/`](../runbooks/)).
- ✅ SLIs defined + alert rules in place.
- ✅ Threat model updated ([`docs/security/stride/`](../security/)).
- ✅ Public API docs regenerated from code.
- ✅ CHANGELOG.md + blueprint §-citation on every change.

---

## 6. Immediate P0 list (next 5 working days)

Ordered by "smallest change with biggest unblock":

1. **Storage service publishes `dms.version.uploaded.v1`** on `CompleteUpload`. [~1 day] — unblocks Phase A and ~40% of Q2-Q3 features.
2. **Audit `actor` column + search `saved_searches` table + connector `active` column** — three analogous schema reconciles, same pattern as today's billing migration. [~1 day] — closes three 500-level endpoints.
3. **Per-service migrations table** (`<svc>_schema_migrations`) rolled out to all 11 services. [~1 day] — unblocks independent service schema evolution forever.
4. **Start the app-profile services (intelligence, preview, collaboration) in the default `docker compose up`** and verify their `/healthz`. [~0.5 day] — shifts from 11/11 to 14/14 healthy.
5. **Spike: Kong in Helm chart + compose** with one protected route. [~2 days] — de-risks Phase B.

Do not start Q2+ feature work until P0 1–4 are green. Do not declare pilot-ready until Phase C is complete.

---

## 7. Traceability

Every ticket/commit under this plan must:
- Cite the blueprint § it advances (`§5.1`, `§8.3`, etc.)
- List the phase letter (A–J) and task number (A1, D3, …)
- Include the blueprint's exit criterion in its acceptance tests

PRs without this traceability are closed, not reviewed.

---

**Net message:** the codebase is further along than a naive count of ✅ would suggest, but further *behind* the blueprint's enterprise bar than any feature-level view shows — because most completions lack the invariants in §4. The fastest path to enterprise isn't more features; it's **closing Phase A+B+C** (≈5 weeks, 4 engineers) to earn the pilot-ready gate, then marching through D→J on the blueprint's calendar.
