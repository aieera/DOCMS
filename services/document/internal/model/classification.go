package model

import (
	"time"

	"github.com/google/uuid"
)

// Classification ladder (§8). Ordered; higher = more sensitive. Empty ("")
// means "unset" and ranks below Unclassified for gating purposes.
const (
	ClassUnclassified = "unclassified"
	ClassInternal     = "internal"
	ClassConfidential = "confidential"
	ClassRestricted   = "restricted"
)

// ClassificationRank maps a level to its ordinal. Unknown/"" → 0 (lowest),
// which is fail-closed for a user's clearance (sees only unclassified content)
// and open for a document's classification (needs no clearance). Mirrors the
// ranks in policy.rego so the Go resolver and the OPA gate agree.
func ClassificationRank(level string) int {
	switch level {
	case ClassInternal:
		return 1
	case ClassConfidential:
		return 2
	case ClassRestricted:
		return 3
	default: // "", "unclassified", or anything unrecognised
		return 0
	}
}

// ValidClassification reports whether s is a settable classification/clearance
// level ("" is allowed as "unset").
func ValidClassification(s string) bool {
	switch s {
	case "", ClassUnclassified, ClassInternal, ClassConfidential, ClassRestricted:
		return true
	}
	return false
}

// ClassificationRule is a tenant-defined classification→access rule: to perform
// Action on a document whose effective classification is ≥ MinClassification
// (or which is PHI-flagged, when AppliesToPHI), the caller must hold at least
// RequiredClearance.
type ClassificationRule struct {
	ID                uuid.UUID
	MinClassification string
	Action            string // '*' | view | view_unredacted | download | share | edit | delete
	RequiredClearance string
	AppliesToPHI      bool
	Description       string
	CreatedAt         time.Time
	CreatedBy         uuid.UUID
}

// ClassificationGateConfig is the per-tenant enablement of classification
// gating. Enabled=false (the default when no row exists) means the read path
// behaves exactly as before.
type ClassificationGateConfig struct {
	Enabled               bool
	PHIRequiresRestricted bool
}

// EffectiveClassification derives the sensitivity floor from the stored level
// plus the PHI/PII flags, so a document the scan flagged as PHI is treated as
// at least Restricted (when the tenant's PHI backstop is on) even if its
// explicit level is unset or lower. This is what makes the DoD's PHI case hold
// without requiring an explicit rule.
func EffectiveClassification(cfg ClassificationGateConfig, class string, hasPHI, hasPII bool) string {
	eff := class
	if hasPHI && cfg.PHIRequiresRestricted && ClassificationRank(ClassRestricted) > ClassificationRank(eff) {
		eff = ClassRestricted
	}
	if hasPII && ClassificationRank(ClassInternal) > ClassificationRank(eff) {
		eff = ClassInternal
	}
	return eff
}

// actionMatches reports whether a rule's action applies to the policy action
// being checked. Downloads are gated through the "view" capability on the read
// path, so a "download" rule also constrains a "view" check.
func actionMatches(ruleAction, checkAction string) bool {
	if ruleAction == "*" || ruleAction == checkAction {
		return true
	}
	return ruleAction == "download" && checkAction == "view"
}

// RequiredClearance resolves the minimum clearance a caller must hold to perform
// checkAction on a document of the given effective classification. It applies
// the strictest matching tenant rule and falls back to the identity ladder
// (to access a level-X document you need level-X clearance). Returns "" when no
// clearance is required.
func RequiredClearance(rules []ClassificationRule, effClass string, hasPHI bool, checkAction string) string {
	req := ""
	if ClassificationRank(effClass) > 0 {
		req = effClass // identity default
	}
	for _, r := range rules {
		if !actionMatches(r.Action, checkAction) {
			continue
		}
		applies := ClassificationRank(effClass) >= ClassificationRank(r.MinClassification)
		if r.AppliesToPHI && hasPHI {
			applies = true
		}
		if !applies {
			continue
		}
		if ClassificationRank(r.RequiredClearance) > ClassificationRank(req) {
			req = r.RequiredClearance
		}
	}
	return req
}

// RiskToClassification maps the intelligence compliance scan's overall_risk
// (ADR 0054: none|low|medium|high|critical) to a sensitivity level for
// denormalisation onto the document row.
func RiskToClassification(overallRisk string, hasPHI bool) string {
	if hasPHI {
		return ClassRestricted
	}
	switch overallRisk {
	case "critical":
		return ClassRestricted
	case "high":
		return ClassConfidential
	case "medium":
		return ClassInternal
	default: // low | none | ""
		return ClassUnclassified
	}
}
