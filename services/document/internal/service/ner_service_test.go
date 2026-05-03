package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
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
