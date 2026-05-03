package service

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func ptrFloat32R(v float32) *float32 { return &v }
func ptrInt32R(v int32) *int32       { return &v }
func ptrBoolR(b bool) *bool          { return &b }

func TestValidateRoutingRuleInput_OK(t *testing.T) {
	require.NoError(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: "Invoices → Finance", CategoryKey: "invoice",
		TargetFolderID: uuid.New(), Priority: 10, Enabled: true,
	}))
}

func TestValidateRoutingRuleInput_RequiredFields(t *testing.T) {
	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		CategoryKey: "invoice", TargetFolderID: uuid.New(),
	}), "name required")

	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: "x", TargetFolderID: uuid.New(),
	}), "category required")

	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: "x", CategoryKey: "invoice",
	}), "folder_id required")
}

func TestValidateRoutingRuleInput_PriorityRange(t *testing.T) {
	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: "x", CategoryKey: "y", TargetFolderID: uuid.New(),
		Priority: 9999,
	}))
	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: "x", CategoryKey: "y", TargetFolderID: uuid.New(),
		Priority: -9999,
	}))
}

func TestValidateRoutingRuleInput_NameLength(t *testing.T) {
	long := make([]byte, 201)
	for i := range long {
		long[i] = 'a'
	}
	require.Error(t, validateRoutingRuleInput(repository.RoutingRuleInput{
		Name: string(long), CategoryKey: "y", TargetFolderID: uuid.New(),
	}))
}

func TestValidateSmartRoutingPatch_OK(t *testing.T) {
	require.NoError(t, validateSmartRoutingPatch(repository.SmartRoutingConfigPatch{
		Enabled:           ptrBoolR(true),
		AutoMoveThreshold: ptrFloat32R(0.95),
		SuggestThreshold:  ptrFloat32R(0.5),
		MaxSuggestions:    ptrInt32R(8),
	}))
}

func TestValidateSmartRoutingPatch_ThresholdRange(t *testing.T) {
	require.Error(t, validateSmartRoutingPatch(repository.SmartRoutingConfigPatch{
		AutoMoveThreshold: ptrFloat32R(1.5),
	}))
	require.Error(t, validateSmartRoutingPatch(repository.SmartRoutingConfigPatch{
		SuggestThreshold: ptrFloat32R(-0.1),
	}))
}

func TestValidateSmartRoutingPatch_MaxSuggestionsRange(t *testing.T) {
	require.Error(t, validateSmartRoutingPatch(repository.SmartRoutingConfigPatch{
		MaxSuggestions: ptrInt32R(0),
	}))
	require.Error(t, validateSmartRoutingPatch(repository.SmartRoutingConfigPatch{
		MaxSuggestions: ptrInt32R(21),
	}))
}
