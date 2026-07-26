# Automatic Document Relationships (invoice ↔ delivery note) — Design

Date: 2026-07-26
Status: approved in conversation ("in both side" + matching strategy "Both,
phased"); this document is the written record.

## Problem

The only relationship feature is the contract graph (ADR 0099), whose edges
are created exclusively by hand (`POST /api/v1/contracts/{id}/edges`). An
invoice and its delivery note are never related automatically — not when both
sync from the ERP (which cannot even sync delivery notes today), not when
uploaded manually. The graph schema anticipated an extractor (confidence <1.0,
nullable created_by, unique triplet for idempotent UPSERT) that was never
built.

## Core idea

**One linking engine in the DMS, two feeders.** The document service gains a
metadata-driven auto-linker; the ERP simply ships richer metadata. The ERP
never calls a linking API.

## Phase 1 (this build) — metadata auto-linker + ERP delivery-note sync

### DMS: `services/document/internal/autolink`

NATS consumer (same pattern as `internal/classification`): durable
subscription on `dms.document.created.v1` and `dms.document.updated.v1`.
Per event: re-establish tenant from the envelope, load the document's
`custom_metadata`, derive match rules, UPSERT `references` edges.

Rules (pure function `DeriveRules(metadata) []Rule`, unit-tested). Fully
GENERIC — the user's requirement is "all documents which have any relation",
not an invoice/delivery-note special case, so no entity type is hardcoded:

1. **Pointer match, confidence 1.0** — for EVERY metadata key of the form
   `erp_<entity>_id` where `<entity>` differs from the doc's own
   `erp_entity_type`: match documents whose metadata contains
   `{erp_entity_type: <entity>, erp_<entity>_id: <value>}`. Covers delivery
   note→invoice, payment→invoice, delivery note→sales order, invoice→quote —
   and every future entity that ships a pointer, with zero code changes.
   Edge direction: carrier → target.
2. **Reverse pointer, confidence 1.0** — the doc's own identity key
   (`erp_<own_entity>_id`) matches OTHER documents carrying that key with the
   same value whose entity type differs. Makes linking symmetric in arrival
   order (invoice arriving after its delivery note still links).
3. **Number match, confidence 0.95** — shared non-empty value across number
   keys: any `erp_*_number`, plus `reference_number` and `document_number`,
   self excluded. This is the standalone path: a manually uploaded delivery
   note whose `reference_number` says INV-123 links to the document carrying
   that number, whatever its type.

Queries use the existing GIN index (`custom_metadata @>` / `->>` equality).
Insert: `ON CONFLICT DO NOTHING` on the unique triplet; `created_by` NULL,
`metadata` records `{rule, matched_key, matched_value}`. Self-loops refused
by the existing CHECK. At-least-once redelivery is safe by construction.

### ERP (crmapp): delivery notes become syncable

- `DMS_ENTITY_REGISTRY.delivery_note`: docType `delivery_note`, numberField
  `delivery_number`, defaultTriggers `['delivered']` (admin-overridable via
  `dms_triggers`), renderPdf via `deliveryNoteService.generatePDF(id)`.
- `deliveryNoteMeta`: `erp_entity_type`, `erp_delivery_note_id`,
  `erp_delivery_number`, **`erp_invoice_id` + `erp_invoice_number`** (joined),
  `erp_sales_order_id`, `erp_customer_id`/name, delivery_date, status.
- Status-transition hook mirrors the invoice module's `maybeEnqueue` call.
- Free rider: `payment_received` already ships `erp_invoice_id`, so payments
  auto-link to invoices as soon as the linker exists.

## Phase 2 (deferred, designed) — content-scan suggestions

Intelligence pipeline gains a reference-extractor over OCR text (document-
number patterns); emits suggestions the same consumer UPSERTs at confidence
0.7, never overwriting higher-confidence edges. The UI already renders
per-edge confidence. Not in this build.

## Testing

- Go: `DeriveRules` table tests (pointer, reverse, number, none, self-skip);
  consumer envelope/tenant handling follows the classification consumer's
  test approach if present, else covered via rule tests + repo integration
  lane.
- CRM (vitest/node): registry entry shape + `deliveryNoteMeta` output.
- Manual: sync invoice then delivery note → Relationships graph shows the
  edge; upload two standalone docs sharing `reference_number` → edge.

## Out of scope

Phase 2 extractor; UI changes (RelationshipsGraph already renders edges);
retro-linking of pre-existing documents (only new/updated docs trigger).
