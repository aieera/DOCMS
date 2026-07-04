# Reconciliation — EPIC roadmap claims vs. on-disk reality

**Date:** 2026-06-28
**Method:** Five parallel file inspections (signature, intelligence, preview,
collaboration, connector), each crux fact re-verified by direct grep before
this report was written. No application code was changed.
**Sources:** roadmap optimism = `docs/PROJECT_STATUS.md`; pessimism =
`docs/STATE_OF_THE_PROJECT.md` truth table (baseline 2026-04-17).

## Headline

The STATE truth table is **stale by ~6–10 weeks**. Three of the five services
it marks ❌/🟡 are substantially built and (in two cases) E2E-verified. Two are
genuinely partial — but not for the reasons the table gives. The roadmap is
closer to reality than STATE on signature, intelligence, and connector; STATE
is closer on preview and collaboration, though even there its specifics are
wrong (collaboration auth is *complete*, not incomplete).

## Classification scheme

- **TRUE (wire-only)** — capability is built; only wiring/config remains. Roadmap's "already built" is essentially right.
- **PARTIAL (foundation + real build)** — real foundation plus real build, but meaningful build work remains.
- **FALSE (net-new build)** — roadmap's "already built" is false; it's a net-new build. STATE's "skeleton" would be right.

## Reconciliation table

| # | Claim | Roadmap says | Disk reality (file:line evidence) | Verdict | Real remaining effort |
|---|---|---|---|---|---|
| 1 | **signature** PAdES LTV | "production-grade PAdES LTV" | **Built.** EU DSS 5.12.1 (eIDAS reference impl) in [build.gradle.kts:40-41](../../services/signature-signer/build.gradle.kts#L40); real PAdES B-LT signing in the Kotlin sidecar; real gRPC client `DSSSidecarSigner.Sign` ([dss_sidecar.go:62](../../services/signature/internal/signer/dss_sidecar.go#L62)); full Go PAdES-LTV validator package (`cms.go`, `crl.go`, `ocsp.go`, `tsa.go`, `ltv.go`) in [services/signature/internal/pades/](../../services/signature/internal/pades/); E2E-verified 2026-06-05. A `MockSigner` exists but is not the production path (selected by `SEDOC_SIGNER`). STATE's "skeleton, no PAdES library" is **wrong**. | **TRUE** (wire-only) | Config, not code: per-tenant KMS signing key + prod TSA URL wiring, prod cert chain. ~0.5–1 day. No net-new crypto. |
| 2 | **intelligence** OCR/NER/auto-file | "already does OCR/NER/auto-filing" | **Built.** OCR = Surya + PaddleOCR fallback ([ocr.py:274,251](../../services/intelligence/app/tasks/ocr.py)); NER = regex + spaCy + opt-in LLM ([ner.py](../../services/intelligence/app/tasks/ner.py), [ner_llm.py:171](../../services/intelligence/app/tasks/ner_llm.py)); embed = sentence-transformers → **Qdrant upsert genuinely called** at [embed.py:349](../../services/intelligence/app/tasks/embed.py#L349) (not stubbed/commented); RAG end-to-end with integration tests (`tests/test_rag_workspace_query.py`). Auto-filing produces **suggestions only** — auto-move is intentionally deferred (ADR 0053). STATE's "Qdrant upsert stubbed / RAG not integration-tested" is **wrong**. | **TRUE** (wire-only) | Enable auto-move behind human approval (ADR 0053 gate) + surface suggestions/RAG in UI + load test. ~2–3 days. No net-new ML. |
| 3 | **preview** render + watermark | watermark rendering happens there | **Mixed.** Real multi-format rasterization (PyMuPDF/pdf2image, LibreOffice headless, Pillow, ffmpeg) → pages stored to S3 with manifest ([tasks/preview.py:116-145](../../services/preview/app/tasks/preview.py)); NATS consumer wired to `dms.version.uploaded.v1`. **But:** zero watermark code anywhere (grep `watermark` = 0 hits), and the frontend never consumes rendered pages — [DocumentViewer](../../web/src/components/viewer/DocumentViewer.tsx) serves the raw blob via the storage proxy; the `/previews/*` REST endpoints are orphaned (grep `/previews` in `web/src` = 0 hits). STATE's "viewer still uses raw PDF" is **correct**. | **PARTIAL** | Net-new watermark overlay (~1–2 days) + wire frontend to consume rendered pages instead of raw blob (~0.5–1 day). ~2–3 days. |
| 4 | **collaboration** | "80% there" | **Mostly built, one real gap.** Yjs CRDT sync via `y-protocols` ([yjs-server.js](../../services/collaboration/src/yjs-server.js)); auth middleware is **real** — verifies session against `/auth/me`, enforces tenant isolation, closes with 4401/4403 ([yjs-server.js:171-209](../../services/collaboration/src/yjs-server.js#L171)); Postgres snapshot persistence (ADR 0096). **Gap:** only `comment_create` is handled ([handler.js:76](../../services/collaboration/src/handler.js#L76)); `comment_update`/`comment_delete` are absent and comments are in-memory broadcast only (lost on restart). STATE's "auth middleware incomplete" is **wrong**; "comment handlers incomplete" is **correct**. | **PARTIAL** | `comment_update`/`comment_delete` handlers + persist comments (NATS/DB) + tests. ~1–2 days (STATE-implied 40–60h is inflated). |
| 5 | **connector** providers | providers "scaffolded" / OAuth "not connected" | **Built — both claims understate.** Webhook delivery real (HMAC-SHA256, exponential backoff, DLQ — [webhook/delivery.go](../../services/connector/internal/webhook/delivery.go)). OAuth **fully connected**: real `authorization_code`/`refresh_token` exchange to live token endpoints ([base.go:54-90](../../services/connector/internal/providers/base.go#L54)) against `login.microsoftonline.com` / `oauth2.googleapis.com`; live Graph/Gmail/Drive/Salesforce REST calls ([m365/client.go](../../services/connector/internal/providers/m365/client.go), [google/google.go](../../services/connector/internal/providers/google/google.go), [salesforce/salesforce.go](../../services/connector/internal/providers/salesforce/salesforce.go)). STATE's "OAuth not connected" is **wrong**. | **TRUE** (wire-only) | Micro: M365 >4MB chunked upload, Salesforce binary streaming. ~1–2 days. No net-new OAuth. |

## Net assessment

- **No service in this set requires a net-new (FALSE) build.** The single
  genuinely-absent feature is the **preview watermark** (and the preview
  frontend wiring) — both inside an otherwise-built service.
- **STATE was wrong in the optimistic direction nowhere** and wrong in the
  pessimistic direction on **signature, intelligence, connector** (whole-status
  errors) and partially on **collaboration** (auth mis-stated as incomplete).
- Aggregate remaining effort across all five gaps is **~7–10 engineer-days**,
  almost entirely wiring/config/UI — not foundational build.

## Caveat on method

Evidence came from focused file inspections; the most consequential reversals
(signature PAdES lib, intelligence Qdrant upsert, connector OAuth exchange,
preview watermark absence, collaboration comment handlers) were re-confirmed by
direct grep before writing. Subagent enthusiasm ("Adobe-accepts", "production")
has been reduced to verifiable facts here. The STATE truth table has been
updated in the same change.
