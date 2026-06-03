package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

func ptrFloat32A(v float32) *float32 { return &v }
func ptrInt32A(v int32) *int32       { return &v }

func TestValidateAnomalyPatch_OK(t *testing.T) {
	require.NoError(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		ZScoreThreshold:          ptrFloat32A(2.5),
		ContentDistanceThreshold: ptrFloat32A(0.7),
		MinDocumentsForAnalysis:  ptrInt32A(20),
	}))
}

func TestValidateAnomalyPatch_ZScoreMustBePositive(t *testing.T) {
	require.Error(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		ZScoreThreshold: ptrFloat32A(0),
	}))
	require.Error(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		ZScoreThreshold: ptrFloat32A(-1.0),
	}))
}

func TestValidateAnomalyPatch_ContentDistanceRange(t *testing.T) {
	require.Error(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		ContentDistanceThreshold: ptrFloat32A(-0.1),
	}))
	require.Error(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		ContentDistanceThreshold: ptrFloat32A(2.5),
	}))
}

func TestValidateAnomalyPatch_MinDocsFloor(t *testing.T) {
	require.Error(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		MinDocumentsForAnalysis: ptrInt32A(4),
	}))
	require.NoError(t, validateAnomalyPatch(repository.AnomalyConfigPatch{
		MinDocumentsForAnalysis: ptrInt32A(5),
	}))
}
