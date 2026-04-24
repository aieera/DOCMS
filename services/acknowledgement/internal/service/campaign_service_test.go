package service

// Pure-Go validation-path tests for CreateCampaign. The tx + repo
// paths are covered by the RLS integration suite (Wave 13.1); here
// we pin the input-shape errors that never reach the DB.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
)

// A Service with no pool is sufficient for the validation-only cases
// because we short-circuit before WithTenantTx.
func newValidationOnlyService(t *testing.T) *Service {
	t.Helper()
	// New() would require a non-nil Config, but for validation-only
	// cases we just need a *Service with a clock and resolver — all
	// other fields stay zero.
	return &Service{
		resolver: StaticRecipientResolver{},
		now:      time.Now,
	}
}

func TestCreateCampaign_MissingTitleValidates(t *testing.T) {
	s := newValidationOnlyService(t)
	_, _, err := s.CreateCampaign(context.Background(), CreateCampaignInput{
		TenantID:   uuid.New(),
		ActorID:    uuid.New(),
		DocumentID: uuid.New(),
		DueAt:      time.Now().Add(48 * time.Hour),
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
}

func TestCreateCampaign_PastDueDateValidates(t *testing.T) {
	s := newValidationOnlyService(t)
	_, _, err := s.CreateCampaign(context.Background(), CreateCampaignInput{
		TenantID:        uuid.New(),
		ActorID:         uuid.New(),
		DocumentID:      uuid.New(),
		Title:           "x",
		DueAt:           time.Now().Add(-time.Hour),
		RecipientPolicy: model.RecipientPolicy{},
	})
	require.Error(t, err)
	require.Equal(t, vdmserr.KindValidation, vdmserr.KindOf(err))
}
