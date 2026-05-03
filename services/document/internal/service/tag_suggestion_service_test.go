package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func ptrBool(b bool) *bool             { return &b }
func ptrFloat32(v float32) *float32    { return &v }
func ptrInt32(v int32) *int32          { return &v }
func ptrStringSlice(v []string) *[]string { return &v }
func ptrJSON(s string) *json.RawMessage {
	r := json.RawMessage(s)
	return &r
}

func TestValidateAutoTagPatch_OK(t *testing.T) {
	err := validateAutoTagPatch(repository.AutoTagConfigPatch{
		Enabled:            ptrBool(true),
		AutoApplyThreshold: ptrFloat32(0.9),
		SuggestThreshold:   ptrFloat32(0.5),
		MaxTagsPerDocument: ptrInt32(15),
		BlockedTags:        ptrStringSlice([]string{"draft"}),
		SourceWeights:      ptrJSON(`{"ner":1.0,"classification":0.8}`),
	})
	require.NoError(t, err)
}

func TestValidateAutoTagPatch_ThresholdRange(t *testing.T) {
	err := validateAutoTagPatch(repository.AutoTagConfigPatch{
		AutoApplyThreshold: ptrFloat32(1.5),
	})
	require.Error(t, err)

	err = validateAutoTagPatch(repository.AutoTagConfigPatch{
		SuggestThreshold: ptrFloat32(-0.1),
	})
	require.Error(t, err)
}

func TestValidateAutoTagPatch_MaxTagsRange(t *testing.T) {
	err := validateAutoTagPatch(repository.AutoTagConfigPatch{
		MaxTagsPerDocument: ptrInt32(0),
	})
	require.Error(t, err)
	err = validateAutoTagPatch(repository.AutoTagConfigPatch{
		MaxTagsPerDocument: ptrInt32(101),
	})
	require.Error(t, err)
}

func TestValidateAutoTagPatch_SourceWeightsShape(t *testing.T) {
	err := validateAutoTagPatch(repository.AutoTagConfigPatch{
		SourceWeights: ptrJSON(`not-json`),
	})
	require.Error(t, err)

	err = validateAutoTagPatch(repository.AutoTagConfigPatch{
		SourceWeights: ptrJSON(`{"ner": 1.5}`),
	})
	require.Error(t, err, "weights above 1.0 must reject")
}
