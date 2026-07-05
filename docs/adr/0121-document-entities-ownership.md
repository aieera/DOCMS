# ADR 0121 — document_entities ownership & isolated-migration gate

**Status:** accepted (2026-07-05)
**Relates to:** ADR 0078 (NER pipeline), STATE_OF_THE_PROJECT 2026-07-03 ("Migration blocker")

## Context

`document_entities` is written by the intelligence service (NER/classify
tasks via `app/persist.py`) and read by the document service
(`internal/repository/ner_repo.go`, the Entities tab, redaction/DLP
confidence policy). Historically only intelligence migration 000002
created the table, while document migration 000021 ALTERed it (the ADR
0078 taxonomy extension: `source` column, `entity_corrections`,
`ner_config`). On a clean database `make migrate-up SERVICE=document`
died at 000021 with `relation "document_entities" does not exist` unless
intelligence's chain happened to have created the table in an earlier
release — an ordering coupling that blocked clean-stack CI and clean
deploys, and broke every document integration test on a fresh
testcontainer.

## Decision

1. **The document schema owns the `document_entities` base table.**
   Document reads it and evolves its taxonomy; document 000021 now
   carries the guarded base definition (`CREATE TABLE IF NOT EXISTS` +
   guarded indexes and RLS policy), column-for-column identical to
   intelligence 000002.
2. **Intelligence 000002 becomes the `IF NOT EXISTS` side** so the
   historically-masked order (intelligence created the table first)
   remains a clean no-op. Editing the already-applied file is safe:
   golang-migrate tracks version numbers, not checksums, and
   environments that already ran it never re-run it.
3. **Rollback stays column-scoped.** Document 000021's down migration
   does not drop the base table — intelligence owns live rows in it on
   any shared database.
4. **Ordering coupling is now gated in CI.**
   `pkg/database/migration_ordering_integration_test.go` pins (a) both
   application orders on a shared database and (b) every service chain
   applying against a brand-new empty database in isolation. Chains with
   pre-existing coupling are allow-listed with tracking issues (audit
   #80, billing #81, intelligence #82, notification #83); an
   allow-listed chain that starts passing fails the gate as a stale
   entry, so each fix must remove its own line.

## Consequences

- Fresh databases bootstrap from the document chain alone; intelligence
  remains dependent on document's `organizations` (issue #82) — document
  is the de-facto bootstrap anchor until that is resolved.
- The two copies of the base definition must stay identical; the
  ordering test fails on drift, and each file's header comment points at
  the other.
- New cross-service table dependencies fail the isolation gate at PR
  time instead of surfacing as a broken clean deploy.
