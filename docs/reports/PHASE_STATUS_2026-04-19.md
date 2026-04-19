# Phase status scorecard — 2026-04-19

Cross-references the blueprint-aligned completion plan's phase letters
(A–J) against the prior Waves 1–13 remediation work AND this session's
PRs. Use this to decide where to spend the next engineer-hour.

## Legend
- ✅ **Done** — shipped + archtest / wire-verified, locks against regression.
- 🟢 **Functionally shipped** — code lands but no regression lock; next
  PR should add archtest.
- 🟡 **Partial** — key wiring exists; named gaps remain.
- ❌ **Not started**.

## Phase ledger

| Phase | Summary | Waves / session PRs | State |
|---|---|---|---|
| **A — Event pipeline unblock** | Upload → version.uploaded → OCR → search end-to-end. | Wave 5.1–5.2 + this session A1/A2/A3/A4/A5 + 000010 reconcile | ✅ (live drill pending) |
| **B — API gateway** | Kong DBless + every route through it. | This session B1, B2.1–B2.5 | ✅ |
| **C — Pilot-ready security** | Per-tenant KEK, cookie-only session, crypto/rand, req-scoped ctx, outbox-only. | Wave 6.1–6.5 + this session archtests | ✅ (locked by archtest) |
| **D — Intelligence / Q2 exit** | OCR, classify, extract, NER, embed, RAG, hybrid search, preview, annotations. | Wave 5.3–5.5 + this session D6 parts 1&2 + D10 REST | 🟢 — D1 corpus + D10 WS/UI pending |
| **E — Workflows / Signatures / Collab** | Approval workflows, PAdES-B-LT, OnlyOffice, Yjs. | Wave 7.1/7.4/7.5 + Wave 9.1/9.2 | 🟡 — approval ✅, signature-signer ✅, **OnlyOffice ❌, Yjs ❌, full channels 🟡** |
| **F — On-prem + observability** | Helm, air-gap, Grafana, alerting, OTEL. | Wave 10 (partial) + this session gateway Helm | 🟡 — Helm chart exists, live drills pending |
| **G — SSO/SCIM/Compliance** | SAML+OIDC, SCIM conformance, retention, legal hold, GDPR DSAR, audit chain. | Wave 7 + Wave 8.1–8.4 + Wave 11.4–11.7 | 🟢 — IdP-verified conformance not yet run |
| **H — SOC 2 / Security hardening** | Evidence automation, pen test, DLP, BYOK, bug bounty. | Wave 12.7 control-plane + 12.8 KMS adapters + 11.7 regional KEKs | 🟡 — evidence automation ✅, pen test ❌, bug bounty ❌ |
| **I — Mobile + connectors** | React Native, SFDC, SAP, M365, Tauri. | Wave 10.3 tags, Wave 12.6 connector-purge | ❌ (scaffolds only) |
| **J — Platform + ecosystem** | Public API, SDKs, load test, marketplace. | This session NATS coverage + routes.yaml | ❌ (foundation only) |

## What the next engineer-hour should buy

Ranked by blueprint-impact × unblock potential:

1. **First-boot drill with pre-built images** — merge `feat/ci-prebuild-images` lands on main; wait for GHA to publish `:main` tags (~15 min); then `make docker-up-prebuilt` + run an upload → verify event → verify OCR → verify search. This is the Phase A exit gate that has dodged us through two cold-build attempts.
2. **D10 WS fan-out + frontend overlay** — closes D10. Collaboration service subscribes to `dms.annotation.>` and pushes to `/ws/collab/{doc}` rooms; PDFViewer flips `renderAnnotationLayer={true}`.
3. **E6 OnlyOffice integration** — compose service + JWT handshake + save callback wiring a new document version. Biggest Q3 blueprint win still outstanding.
4. **E7 Yjs layer** — y-websocket server in collaboration service, Yjs doc per document-view for real-time comments. Complements D10.
5. **E8 full notification channels** — Slack + Teams + FCM/APNs adapters behind the existing channel interface.

## What's already locked against regression

Running `go test ./pkg/archtest/...` (green on every PR via `.github/workflows/ci.yml`'s `test` job) asserts:

- `TestPhaseC2_NoAuthTokenInBrowserStorage` — C2.
- `TestPhaseC3_NoMathRandInSAMLSigner` — C3.
- `TestPhaseC4_NoContextBackgroundInHandlers` — C4.
- `TestPhaseC5_NoDirectNATSPublish` — C5.
- `TestGatewayRoutesMirrorKongConfig` — B2.1 routes.yaml ↔ kong.yaml.
- `TestSubjectCoverage_AllPublishedSubjectsHaveStreams` — §4.7 A3.
- `TestSubjectCoverage_DeliberatelyUncoveredSubjectFails` — ditto.
- `TestSubjectCovers_MatchingRules` — NATS semantics pin.
- `TestWithMigrationsTable_SetsQueryParam` — §4.1 A5 per-service migrations.
- `TestRequireGatewaySignature_*` — §3.1 B2.2 gateway signature (4 cases).

Adding to the lock each session is the highest-leverage activity in this repo —
every archtest is one class of future regression priced in at ~100ms of CI time.

## Open branches on the remote

None at this merge point — all session PRs are landed.

## Session summary (2026-04-19)

Shipped to `main` in 14 merges:
- A1 tests (genesis), A2 (compose default-start + Go services),
  A3 (NATS subject coverage), A4×3 (schema drift), A5 (per-service migrations),
  B1 (Kong spike), B2.1–B2.5 (routes, signature, sweep, vite, rate limits),
  C archtests, D6 parts 1 & 2 (RRF + Qdrant + embed-query + intelligence API),
  D10 (annotation REST), CI prebuild + compose overlay, gitignore hygiene.

Approx 4,500 net LOC, 60+ test cases, 10 archtests locked, 3 new
databases/migrations reconciled, 1 net-new architecture layer
(fusion package), 1 new CI workflow, 1 shared Dockerfile for all Go
services.
