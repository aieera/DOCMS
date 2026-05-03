package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/document/internal/repository"
)

func ptrUUID(v uuid.UUID) *uuid.UUID { return &v }

func TestValidateCorrectEntity_Confirm(t *testing.T) {
	require.NoError(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "confirm",
		CorrectedType: "email",
	}))
}

func TestValidateCorrectEntity_RelabelOK(t *testing.T) {
	require.NoError(t, validateCorrectEntity(CorrectEntityInput{
		Action:           "relabel",
		OriginalEntityID: ptrUUID(uuid.New()),
		OriginalType:     "name",
		CorrectedType:    "party_name",
	}))
}

func TestValidateCorrectEntity_AddRequiresValue(t *testing.T) {
	require.Error(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "add",
		CorrectedType: "email",
		StartOffset:   0,
		EndOffset:     5,
	}))
}

func TestValidateCorrectEntity_AddRequiresValidSpan(t *testing.T) {
	require.Error(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "add",
		CorrectedType: "email",
		EntityValue:   "x@y.io",
		StartOffset:   10,
		EndOffset:     5,
	}))
}

func TestValidateCorrectEntity_BadAction(t *testing.T) {
	require.Error(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "ignore",
		CorrectedType: "email",
	}))
}

func TestValidateCorrectEntity_UnknownType(t *testing.T) {
	require.Error(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "relabel",
		CorrectedType: "made_up",
	}))
}

func TestValidateCorrectEntity_NoteTooLong(t *testing.T) {
	require.Error(t, validateCorrectEntity(CorrectEntityInput{
		Action:        "confirm",
		CorrectedType: "email",
		Note:          strings.Repeat("x", 1001),
	}))
}

func TestValidateCorrectEntity_DeleteSkipsTypeCheck(t *testing.T) {
	// 'delete' doesn't need corrected_type.
	require.NoError(t, validateCorrectEntity(CorrectEntityInput{
		Action: "delete",
	}))
}

// ---- NER config validator ------------------------------------------------

func ptrBoolNER(v bool) *bool       { return &v }
func ptrStrNER(v string) *string    { return &v }
func ptrInt32NER(v int32) *int32    { return &v }
func ptrFloat32NER(v float32) *float32 { return &v }
func ptrTypes(v []string) *[]string  { return &v }

func TestValidateNERConfig_OK(t *testing.T) {
	require.NoError(t, validateNERConfigPatch(repository.NERConfigPatch{
		Enabled:       ptrBoolNER(true),
		Model:         ptrStrNER("claude-haiku-4-5"),
		BatchSize:     ptrInt32NER(5),
		MinConfidence: ptrFloat32NER(0.7),
		EntityTypes:   ptrTypes([]string{"governing_law", "icd_code"}),
	}))
}

func TestValidateNERConfig_BatchSizeRange(t *testing.T) {
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{BatchSize: ptrInt32NER(0)}))
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{BatchSize: ptrInt32NER(21)}))
}

func TestValidateNERConfig_MinConfidenceRange(t *testing.T) {
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{MinConfidence: ptrFloat32NER(-0.1)}))
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{MinConfidence: ptrFloat32NER(1.1)}))
}

func TestValidateNERConfig_EmptyModelRejected(t *testing.T) {
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{Model: ptrStrNER("   ")}))
}

func TestValidateNERConfig_UnknownEntityTypeRejected(t *testing.T) {
	require.Error(t, validateNERConfigPatch(repository.NERConfigPatch{
		EntityTypes: ptrTypes([]string{"governing_law", "made_up_type"}),
	}))
}

func TestIsPIIType(t *testing.T) {
	for _, tc := range []struct {
		t    string
		want bool
	}{
		{"email", true},
		{"phone", true},
		{"governing_law", false},
		{"icd_code", false},
		{"patient_id", true},
	} {
		require.Equal(t, tc.want, isPIIType(tc.t), tc.t)
	}
}
