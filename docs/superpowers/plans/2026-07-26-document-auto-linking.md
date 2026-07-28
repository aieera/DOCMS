# Document Auto-Linking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Automatically create `references` edges between related documents (generic: any `erp_<entity>_id` pointer or shared document-number metadata), fed by DMS uploads and by ERP sync — which also gains delivery-note support.

**Architecture:** New `internal/autolink` NATS consumer in the document service (pattern: `internal/classification`), consuming `dms.document.created.v1` + `dms.document.updated.v1`. Pure rule derivation + thin SQL (GIN `custom_metadata` matching, UPSERT into `contract_graph_edges` ON CONFLICT DO NOTHING). CRM adds `delivery_note` to the sync registry with invoice/sales-order pointer metadata.

**Tech Stack:** Go 1.25 (document service), NATS JetStream, Postgres JSONB; Node (crmapp registry) with its existing node:test/jest suite.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-26-document-auto-linking-design.md`.
- All DB work inside `database.WithTenantTx` (RLS); NATS consumers re-establish tenant from the envelope (CLAUDE.md).
- No new endpoints; no direct NATS publish from handlers (outbox rule untouched — consumer only INSERTS edges).
- Go tests: `go test -race ./services/document/internal/autolink/`.

---

### Task 1: Rule derivation (pure) — `DeriveRules`

**Files:** Create `services/document/internal/autolink/rules.go`, `rules_test.go`.

**Interfaces (produces):**
```go
type Rule struct {
    Kind       string  // "pointer" | "reverse-pointer" | "number"
    Key        string  // metadata key on the MATCH TARGET side
    Value      string
    EntityType string  // non-empty for pointer kinds: required erp_entity_type of target
    Confidence float64 // 1.0 pointers, 0.95 number
    Outbound   bool    // true: src=this doc; false: src=matched doc
}
func DeriveRules(meta map[string]any) []Rule
```

- [ ] Table tests: delivery-note meta (erp_invoice_id + erp_sales_order_id) → two outbound pointer rules + reverse for own id + number rules; invoice meta → reverse pointer for erp_invoice_id + number rule for erp_invoice_number; `reference_number`/`document_number` produce number rules; empty/self keys skipped; non-string values skipped.
- [ ] RED → implement (regex `^erp_(.+)_id$`, own entity from `erp_entity_type`; number keys: `^erp_.+_number$`, `reference_number`, `document_number`) → GREEN → commit.

### Task 2: Repo — match + upsert

**Files:** Create `services/document/internal/autolink/repo.go`.

```go
type Repo struct{}
func (Repo) DocumentMetadata(ctx, tx, tenantID, docID) (map[string]any, error)      // SELECT custom_metadata FROM documents WHERE id=$2
func (Repo) FindPointerTargets(ctx, tx, tenantID, r Rule, self uuid.UUID) ([]uuid.UUID, error)
// pointer: WHERE custom_metadata @> jsonb_build_object('erp_entity_type', r.EntityType, r.Key, r.Value)
// reverse/number: WHERE custom_metadata->>r.Key = r.Value AND id <> self
//                 (reverse additionally: custom_metadata->>'erp_entity_type' IS DISTINCT FROM own type — pass own type in Rule.EntityType)
func (Repo) InsertEdge(ctx, tx, tenantID, src, dst uuid.UUID, confidence float64, ruleMeta map[string]any) error
// INSERT INTO contract_graph_edges (tenant_id, src_document, dst_document, edge_type, confidence, metadata)
// VALUES ($1,$2,$3,'references',$4,$5) ON CONFLICT DO NOTHING; skip src==dst
```

- [ ] SQL is thin; covered by Task 3's consumer test via an interface fake + by the integration lane. Commit with Task 3.

### Task 3: Consumer

**Files:** Create `services/document/internal/autolink/consumer.go`, `consumer_test.go`.

- [ ] Test with a fake store (interface over the three repo methods): event {tenant, doc} → metadata with erp_invoice_id → expects InsertEdge(src=doc, dst=matched, conf 1.0); number rule both-direction dedup (same pair only once per handle); malformed envelope → Ack (no work); repo error → Nak.
- [ ] Implement: subscribe `dms.document.created.v1` + `dms.document.updated.v1` (durables `document-autolink-created`, `document-autolink-updated`, ManualAck, AckWait 2m); handle: parse env → tenant + document_id → WithTenantTx: load metadata → DeriveRules → for each rule find targets → InsertEdge (Outbound decides src/dst). Cap targets per rule at 25 (log when capped — no silent truncation).
- [ ] Commit.

### Task 4: Wire into main.go

- [ ] `services/document/cmd/server/main.go` next to classification consumer start (~line 1531): `autolink.NewConsumer(js, pool, *log.Z()).Start()` — same non-fatal log-and-continue contract. Build + commit.

### Task 5: CRM delivery-note sync

**Files:** Modify `crmapp/Backend/src/core/dmsSync/dmsConfig.js` (registry entry), `customMetadata.js` (deliveryNoteMeta + builder map), `pushEnrichment.js` (ENTITY_MODELS/joins/idKey/tags for delivery_note), DeliveryNotes service status-transition hook (mirror invoice's `maybeEnqueue`); test alongside existing dmsSync tests.

- [ ] Registry: docType/numberField `delivery_number`, defaultTriggers `['delivered']`, renderPdf via `deps.deliveryNoteService.generatePDF(id)` (verify return shape at call time — wrap to `{buffer, documentNumber}`).
- [ ] `deliveryNoteMeta(dn, customer, invoice)` emits erp_entity_type, erp_delivery_note_id, erp_delivery_number, erp_invoice_id/number, erp_sales_order_id, erp_customer_id, delivery/status fields.
- [ ] Test: registry entry present + meta output shape. Run the CRM dmsSync test file. Commit (crmapp repo).

### Task 6: Deploy + verify

- [ ] Rebuild document service container (`docker compose up -d --build document` from DOCMS) so the consumer runs live; CRM backend restart per its process manager.
- [ ] Manual: push invoice + delivery note (or two uploads sharing reference_number) → edge appears in the Relationships tab.
