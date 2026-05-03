# Architecture Decision Records — Index

Format: Michael Nygard (Context · Decision · Consequences · Alternatives).
Numbering is monotonic and gap-free. Once merged, ADRs are immutable —
subsequent decisions that overturn an ADR supersede it via a new ADR.

## Status legend

- **Accepted** — merged and in force.
- **Proposed** — under review, not yet binding.
- **Superseded** — replaced; see the newer ADR.
- **Deprecated** — still documented but no longer guiding current work.

## Register

| # | Title | Status | Date |
|---|---|---|---|
| [0021](0021-version-uploaded-event-emission-point.md) | `dms.version.uploaded.v1` is emitted from document service, not storage | Accepted | 2026-04-17 |
| [0022](0022-per-tenant-kek-derivation.md) | Per-tenant KEK via HKDF-SHA256 from master secret (dev/on-prem); Vault/AWS KMS alias (prod) | Accepted | 2026-04-17 |
| [0023](0023-temporal-namespace-strategy.md) | Single Temporal namespace `vaultdms` + `tenant_id` search attribute + workflow-id prefix | Accepted | 2026-04-17 |
| [0024](0024-gdpr-dsr-strategy.md) | GDPR DSR: per-request Temporal workflow, hold short-circuit, 7-year ledger, HMAC anonymization | Accepted | 2026-04-17 |
| [0025](0025-pades-library.md) | PAdES signing via EU Commission DSS (LGPL-2.1) as a Java sidecar; fallback Digidoc4j / commercial iText | Accepted | 2026-04-17 |
| [0026](0026-per-region-kek-masters.md) | Per-region KEK master secrets; kekID gains `/region` suffix; cross-region isolation under ikm compromise | Accepted | 2026-04-18 |
| [0052](0052-auto-tagging.md) | Auto-tagging from NER + classification; per-tenant thresholds; never auto-apply without admin-configured `auto_apply_threshold` | Accepted | 2026-05-03 |
| [0053](0053-smart-routing.md) | Smart routing via three strategies (rule, history, similarity); never auto-moves in v1; folder placement always traverses document-service permission/lifecycle gates | Accepted | 2026-05-03 |
| [0054](0054-compliance-pii-detection.md) | Compliance scanning: NER + regex PII/PHI detection, per-tenant config, never auto-holds (recommends only), redacted sample context, encrypted values column reserved for v2 | Accepted | 2026-05-03 |
| [0055](0055-document-qa-chat.md) | Document Q&A chat panel: SSE streaming + sync fallback, conversations persisted in qa_conversations + qa_messages, citations carry page/start/end_char for PDF viewer highlight, multi-turn context capped at last 6 messages | Accepted | 2026-05-03 |
| [0056](0056-translation-pipeline.md) | Translation pipeline: auto langdetect post-OCR, on-demand LLM translation via chunked calls, per-tenant cost guard via max_chars_per_doc, translations are separate artifacts keyed on (version, target_language) | Accepted | 2026-05-03 |
| [0057](0057-ocr-quality-scoring.md) | OCR quality scoring: 5 sub-scores composite + 4-tier grade per-page + per-doc summary, language-agnostic heuristic (no dictionary), single-shot auto-retry with engine swap, opt-in poor-grade notification | Accepted | 2026-05-03 |
| [0058](0058-anomaly-detection.md) | Anomaly detection: workspace-level outlier scan across metadata (z-score), content (Qdrant centroid distance), and behavioral signals; manual REST trigger only in v1 (no Beat); never auto-acts | Accepted | 2026-05-03 |

*Note: 0021 was promoted from its final.md-placeholder slot to document
an actual Wave 5 decision. Wave 6.5 shipped without an ADR (no design
decision required — the pattern was already established). Subsequent
numbers shift by one.*

## Template

Copy into `docs/adr/NNNN-<slug>.md`:

```markdown
# NNNN — <Title>

- **Status:** Proposed
- **Date:** YYYY-MM-DD
- **Supersedes:** — (or ADR NNNN)
- **Deciders:** <names>

## Context
<The forces at play, including technological, political, social, and
project local. Describe the problem as a neutral observer.>

## Decision
<The response to these forces, in full sentences, active voice.>

## Consequences
<What becomes easier or harder because of this decision.>

## Alternatives considered
<Each alternative with pros/cons and why it was rejected.>

## Sources
<Links to RFCs, papers, prior art, CVEs consulted.>
```
