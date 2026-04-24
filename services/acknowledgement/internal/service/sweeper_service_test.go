package service

// Sweeper-layer pure-Go check. The real UPDATE + outbox insert path
// is exercised by the RLS integration suite; here we pin that the
// zero-result return shape is safe to serialise.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSweepRemindersResult_JSONRoundtrip(t *testing.T) {
	// The handler wraps SweepReminders' result in a JSON response,
	// so the struct must encode + decode cleanly.
	in := SweepRemindersResult{Reminded: 3, Escalated: 1}
	b, err := json.Marshal(in)
	require.NoError(t, err)

	var out SweepRemindersResult
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}
