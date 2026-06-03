package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/repository"
)

func ptrFloat32O(v float32) *float32 { return &v }
func ptrBoolO(b bool) *bool          { return &b }

func TestValidateOCRQualityPatch_OK(t *testing.T) {
	require.NoError(t, validateOCRQualityPatch(repository.OCRQualityConfigPatch{
		Enabled:            ptrBoolO(true),
		ReviewThreshold:    ptrFloat32O(0.5),
		ExcellentThreshold: ptrFloat32O(0.9),
		GoodThreshold:      ptrFloat32O(0.75),
		FairThreshold:      ptrFloat32O(0.6),
		AutoRetryBelow:     ptrFloat32O(0.3),
	}))
}

func TestValidateOCRQualityPatch_OutOfRange(t *testing.T) {
	for _, p := range []repository.OCRQualityConfigPatch{
		{ReviewThreshold: ptrFloat32O(1.5)},
		{ExcellentThreshold: ptrFloat32O(-0.1)},
		{GoodThreshold: ptrFloat32O(2.0)},
		{FairThreshold: ptrFloat32O(-0.5)},
		{AutoRetryBelow: ptrFloat32O(1.1)},
	} {
		require.Error(t, validateOCRQualityPatch(p))
	}
}
