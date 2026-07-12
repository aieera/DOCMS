package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/policy/internal/model"
)

// A deny must be logged with the full who/what/why so a refused access is
// auditable from the decision log.
func TestLogDecision_DenyRecordsWhoWhatWhy(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{log: zerolog.New(&buf)}

	tenant := uuid.Must(uuid.NewV7())
	ctx := auth.SetCorrelationID(context.Background(), "corr-123")
	in := CheckInput{
		TenantID:     tenant,
		SubjectType:  "user",
		SubjectID:    "user-42",
		Action:       "delete",
		ResourceType: "document",
		ResourceID:   "doc-7",
	}
	s.logDecision(ctx, in, model.CheckResult{Allowed: false, Reason: "no matching grant"})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry), "log line must be structured JSON")
	require.Equal(t, "deny", entry["decision"])
	require.Equal(t, tenant.String(), entry["tenant_id"])
	// who
	require.Equal(t, "user", entry["subject_type"])
	require.Equal(t, "user-42", entry["subject_id"])
	// what
	require.Equal(t, "delete", entry["action"])
	require.Equal(t, "document", entry["resource_type"])
	require.Equal(t, "doc-7", entry["resource_id"])
	// why
	require.Equal(t, "no matching grant", entry["reason"])
	// traceability
	require.Equal(t, "corr-123", entry["correlation_id"])
}

// An empty rego reason must still produce a non-empty why.
func TestLogDecision_DenyDefaultsReason(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{log: zerolog.New(&buf)}
	s.logDecision(context.Background(), CheckInput{
		TenantID: uuid.Must(uuid.NewV7()), SubjectType: "user", SubjectID: "u",
		Action: "view", ResourceType: "document", ResourceID: "d",
	}, model.CheckResult{Allowed: false, Reason: ""})

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	require.NotEmpty(t, entry["reason"], "a deny must always carry a why")
}

// Allows are debug-only: the decision log (info) exists to explain refusals,
// so an allow must not add noise at info level.
func TestLogDecision_AllowIsQuietAtInfo(t *testing.T) {
	var buf bytes.Buffer
	s := &Service{log: zerolog.New(&buf).Level(zerolog.InfoLevel)}
	s.logDecision(context.Background(), CheckInput{
		TenantID: uuid.Must(uuid.NewV7()), SubjectType: "user", SubjectID: "u",
		Action: "view", ResourceType: "document", ResourceID: "d",
	}, model.CheckResult{Allowed: true})
	require.Empty(t, buf.String(), "an allow must not emit an info-level decision log line")
}
