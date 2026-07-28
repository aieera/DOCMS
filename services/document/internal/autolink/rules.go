// Package autolink derives document relationships from custom_metadata
// and materialises them as `references` edges in the contract graph
// (ADR 0099 anticipated an extractor: confidence <1.0, NULL created_by,
// unique triplet for idempotent UPSERT — this is it).
//
// The rules are deliberately generic — "all documents which have any
// relation", not an invoice/delivery-note special case. Relationships
// come from two metadata shapes:
//
//   - ERP pointer keys (erp_<entity>_id) written by the CRM sync's
//     metadata builders, linking a document to the entity it derives
//     from (delivery note → invoice, payment → invoice, …).
//   - Shared document numbers (erp_*_number, reference_number,
//     document_number) — the standalone path for manual uploads.
package autolink

import (
	"regexp"
	"strings"
)

// Rule is one match instruction derived from a document's metadata.
type Rule struct {
	// Kind: "pointer" (this doc carries a foreign id), "reverse-pointer"
	// (other docs may carry a pointer to this doc's own id), or "number"
	// (shared document-number value).
	Kind string
	// Key is the metadata key on the MATCH TARGET side.
	Key   string
	Value string
	// EntityType: for "pointer", the required erp_entity_type of the
	// target; for "reverse-pointer", this doc's OWN entity type (targets
	// must differ). Empty for "number".
	EntityType string
	Confidence float64
	// Outbound: true → edge src is this document (it references the
	// target); false → src is the matched document.
	Outbound bool
}

var (
	erpIDKey     = regexp.MustCompile(`^erp_(.+)_id$`)
	erpNumberKey = regexp.MustCompile(`^erp_.+_number$`)
)

// DeriveRules maps a document's custom_metadata to match rules. Pure —
// no I/O — so the whole linking policy is table-testable.
func DeriveRules(meta map[string]any) []Rule {
	ownType, _ := meta["erp_entity_type"].(string)
	var rules []Rule

	for key, raw := range meta {
		val, ok := raw.(string)
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}

		if m := erpIDKey.FindStringSubmatch(key); m != nil {
			entity := m[1]
			if entity == "entity" { // erp_entity_id would be nonsense; guard anyway
				continue
			}
			if ownType != "" && entity == ownType {
				// The document's own identity key: others point at US.
				rules = append(rules, Rule{
					Kind: "reverse-pointer", Key: key, Value: val,
					EntityType: ownType, Confidence: 1.0, Outbound: false,
				})
			} else {
				// A foreign key: we point at the entity's document.
				rules = append(rules, Rule{
					Kind: "pointer", Key: key, Value: val,
					EntityType: entity, Confidence: 1.0, Outbound: true,
				})
			}
			continue
		}

		if erpNumberKey.MatchString(key) || key == "reference_number" || key == "document_number" {
			rules = append(rules, Rule{
				Kind: "number", Key: key, Value: val,
				Confidence: 0.95, Outbound: true,
			})
		}
	}
	return rules
}
