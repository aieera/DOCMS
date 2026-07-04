// Classification / clearance gate tests (§8). These lock the DoD: a
// PHI-classified document is blocked for an uncleared user and allowed for a
// cleared one, and the denial carries an explainable reason. Pure-Go against
// the compiled Rego — no DB.
package opa

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/policy/internal/model"
)

// A user with a direct view grant on a PHI document is still blocked when they
// lack the required clearance — classification deny overrides the ACL allow.
func TestPHIDocBlockedForUnclearedUser(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"classification_gate":     "on",
			"has_phi":                 "true",
			"security_classification": "restricted",
			"required_clearance":      "restricted",
			"user_clearance":          "internal", // below restricted
		},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	res := eval(t, e, in, perms, nil, nil)
	require.False(t, res.Allowed, "uncleared user must be blocked from a PHI doc")
	require.Contains(t, res.Reason, "PHI", "reason must explain the PHI block")
	require.Contains(t, strings.ToLower(res.Reason), "clearance")
}

// The same document is viewable once the user holds sufficient clearance.
func TestPHIDocAllowedForClearedUser(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"classification_gate":     "on",
			"has_phi":                 "true",
			"security_classification": "restricted",
			"required_clearance":      "restricted",
			"user_clearance":          "restricted", // meets the bar
		},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed, "cleared user must be allowed")
}

// The org owner is exempt (break-glass) even without clearance.
func TestOwnerBypassesClassificationGate(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "owner1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"classification_gate": "on",
			"has_phi":             "true",
			"required_clearance":  "restricted",
			"user_clearance":      "",
			"user_role":           "owner",
		},
	}
	require.True(t, eval(t, e, in, nil, nil, nil).Allowed, "owner is break-glass exempt")
}

// With gating disabled the classification context is ignored entirely (existing
// tenants pay nothing and behave as before).
func TestGateDisabledIgnoresClassification(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "view", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"classification_gate": "off",
			"has_phi":             "true",
			"required_clearance":  "restricted",
			"user_clearance":      "",
		},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	require.True(t, eval(t, e, in, perms, nil, nil).Allowed, "disabled gate must not block")
}

// A non-PHI confidential document names the classification level in its reason.
func TestConfidentialDocReasonNamesClassification(t *testing.T) {
	e := mustEngine(t)
	in := model.CheckInput{
		SubjectType: "user", SubjectID: "u1",
		Action: "download", ResourceType: "document", ResourceID: "d1",
		Context: map[string]string{
			"classification_gate":     "on",
			"has_phi":                 "false",
			"security_classification": "confidential",
			"required_clearance":      "confidential",
			"user_clearance":          "internal",
		},
	}
	perms := []PermissionDoc{{
		ResourceType: "document", ResourceID: "d1",
		PrincipalType: "user", PrincipalID: "u1",
		Capability: "view",
	}}
	res := eval(t, e, in, perms, nil, nil)
	require.False(t, res.Allowed)
	require.Contains(t, res.Reason, "confidential")
}
