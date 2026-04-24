package service

// Assignment-layer pure-Go check. Happy-path + forbidden-path tests
// that touch the DB live in the RLS integration suite; this test
// pins the AcknowledgeInput JSON shape because the handler
// decodes the caller's request body directly into it.

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAcknowledgeInput_JSONRoundtrip(t *testing.T) {
	in := AcknowledgeInput{
		TenantID:     uuid.New(),
		ActorID:      uuid.New(),
		AssignmentID: uuid.New(),
		IPAddress:    "198.51.100.9",
		UserAgent:    "Mozilla/5.0",
		Comment:      "I have read and understood.",
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)

	var out AcknowledgeInput
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}
