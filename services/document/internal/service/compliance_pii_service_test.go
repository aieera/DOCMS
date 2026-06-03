package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

func ptrJSONC(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func ptrStringSliceC(v []string) *[]string { return &v }

func TestValidateComplianceConfigPatch_OK(t *testing.T) {
	require.NoError(t, validateComplianceConfigPatch(repository.ComplianceConfigPatch{
		NotifyRoles:            ptrStringSliceC([]string{"compliance_officer"}),
		PIIEntityRiskOverrides: ptrJSONC(`{"EMAIL":"high","SSN":"critical"}`),
		CustomPatterns:         ptrJSONC(`[{"type":"TICKET","regex":"T-\\d+"}]`),
	}))
}

func TestValidateComplianceConfigPatch_NotifyRolesEmpty(t *testing.T) {
	require.Error(t, validateComplianceConfigPatch(repository.ComplianceConfigPatch{
		NotifyRoles: ptrStringSliceC([]string{}),
	}))
}

func TestValidateComplianceConfigPatch_RiskOverrideShape(t *testing.T) {
	require.Error(t, validateComplianceConfigPatch(repository.ComplianceConfigPatch{
		PIIEntityRiskOverrides: ptrJSONC(`not-json`),
	}))
	require.Error(t, validateComplianceConfigPatch(repository.ComplianceConfigPatch{
		PIIEntityRiskOverrides: ptrJSONC(`{"EMAIL":"super-bad"}`),
	}), "invalid risk level must reject")
}

func TestValidateComplianceConfigPatch_CustomPatternCap(t *testing.T) {
	patterns := make([]map[string]string, 0, 33)
	for i := 0; i < 33; i++ {
		patterns = append(patterns, map[string]string{"type": "T", "regex": "x"})
	}
	b, _ := json.Marshal(patterns)
	require.Error(t, validateComplianceConfigPatch(repository.ComplianceConfigPatch{
		CustomPatterns: ptrJSONC(string(b)),
	}))
}
