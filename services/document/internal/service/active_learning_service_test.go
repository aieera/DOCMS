package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

func ptrFloat32AL(v float32) *float32 { return &v }
func ptrInt32AL(v int32) *int32       { return &v }

func TestValidateActiveLearningPatch_OK(t *testing.T) {
	require.NoError(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		MinExamplesForRetrain:  ptrInt32AL(50),
		RetrainIncrement:       ptrInt32AL(25),
		MinAccuracyImprovement: ptrFloat32AL(0.02),
		TrainValidationSplit:   ptrFloat32AL(0.10),
		TrainTestSplit:         ptrFloat32AL(0.10),
	}))
}

func TestValidateActiveLearningPatch_MinExamplesFloor(t *testing.T) {
	require.Error(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		MinExamplesForRetrain: ptrInt32AL(9),
	}))
	require.NoError(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		MinExamplesForRetrain: ptrInt32AL(10),
	}))
}

func TestValidateActiveLearningPatch_RetrainIncrementMin(t *testing.T) {
	require.Error(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		RetrainIncrement: ptrInt32AL(0),
	}))
}

func TestValidateActiveLearningPatch_NegativeImprovement(t *testing.T) {
	require.Error(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		MinAccuracyImprovement: ptrFloat32AL(-0.01),
	}))
}

func TestValidateActiveLearningPatch_SplitRange(t *testing.T) {
	require.Error(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		TrainValidationSplit: ptrFloat32AL(-0.1),
	}))
	require.Error(t, validateActiveLearningPatch(repository.ActiveLearningConfigPatch{
		TrainTestSplit: ptrFloat32AL(0.6),
	}))
}
