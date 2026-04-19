package model_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// TestValidateTransition is an exhaustive table of every (from, action) pair
// we support, plus a representative sample of invalid combinations. Adding a
// new transition? Add a row and a deliberate "negative" row for the actions
// that should still reject from the new state.
func TestValidateTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    model.LifecycleState
		action  model.LifecycleAction
		want    model.LifecycleState
		wantErr bool
	}{
		// happy path: every valid transition
		{"draft -> submit_for_review = in_review", model.StateDraft, model.ActionSubmitForReview, model.StateInReview, false},
		{"in_review -> approve = active", model.StateInReview, model.ActionApprove, model.StateActive, false},
		{"in_review -> reject = draft", model.StateInReview, model.ActionReject, model.StateDraft, false},
		{"active -> supersede = superseded", model.StateActive, model.ActionSupersede, model.StateSuperseded, false},
		{"active -> archive = retained", model.StateActive, model.ActionArchive, model.StateRetained, false},
		{"superseded -> archive = retained", model.StateSuperseded, model.ActionArchive, model.StateRetained, false},
		{"retained -> archive = archived", model.StateRetained, model.ActionArchive, model.StateArchived, false},
		{"retained -> restore = active", model.StateRetained, model.ActionRestore, model.StateActive, false},
		{"archived -> dispose = disposed", model.StateArchived, model.ActionDispose, model.StateDisposed, false},

		// legal hold: enterable from every non-disposed state
		{"draft -> apply_hold = legal_hold", model.StateDraft, model.ActionApplyHold, model.StateLegalHold, false},
		{"in_review -> apply_hold", model.StateInReview, model.ActionApplyHold, model.StateLegalHold, false},
		{"active -> apply_hold", model.StateActive, model.ActionApplyHold, model.StateLegalHold, false},
		{"superseded -> apply_hold", model.StateSuperseded, model.ActionApplyHold, model.StateLegalHold, false},
		{"retained -> apply_hold", model.StateRetained, model.ActionApplyHold, model.StateLegalHold, false},
		{"archived -> apply_hold", model.StateArchived, model.ActionApplyHold, model.StateLegalHold, false},
		// release_hold returns the sentinel draft; service overrides with the stored prev state.
		{"legal_hold -> release_hold = draft sentinel", model.StateLegalHold, model.ActionReleaseHold, model.StateDraft, false},

		// Invalid transitions
		{"draft -> approve (invalid)", model.StateDraft, model.ActionApprove, "", true},
		{"draft -> dispose (invalid)", model.StateDraft, model.ActionDispose, "", true},
		{"active -> submit_for_review (invalid)", model.StateActive, model.ActionSubmitForReview, "", true},
		{"active -> approve (invalid)", model.StateActive, model.ActionApprove, "", true},
		{"disposed -> apply_hold (invalid)", model.StateDisposed, model.ActionApplyHold, "", true},
		{"disposed -> restore (invalid)", model.StateDisposed, model.ActionRestore, "", true},
		{"legal_hold -> archive (invalid)", model.StateLegalHold, model.ActionArchive, "", true},
		{"legal_hold -> dispose (invalid)", model.StateLegalHold, model.ActionDispose, "", true},
		{"retained -> dispose (invalid — must archive first)", model.StateRetained, model.ActionDispose, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.ValidateTransition(tc.from, tc.action)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestIsLegalHoldBlocked documents the exact operation policy under hold.
// The legal / records-management team owns this list; any change should be
// reviewed by them.
func TestIsLegalHoldBlocked(t *testing.T) {
	// Not under hold → nothing blocked.
	for _, op := range []string{"delete", "create_version", "move", "view", "download", "update_title"} {
		require.False(t, model.IsLegalHoldBlocked(model.StateActive, op), "active state never blocks %s", op)
	}
	// Under hold → the blocked set
	for _, op := range []string{"delete", "hard_delete", "create_version", "move"} {
		require.True(t, model.IsLegalHoldBlocked(model.StateLegalHold, op), "legal_hold must block %s", op)
	}
	// Under hold → the allowed set
	for _, op := range []string{"view", "download", "comment", "annotate", "update_title", "update_metadata", "list_versions"} {
		require.False(t, model.IsLegalHoldBlocked(model.StateLegalHold, op), "legal_hold must NOT block %s", op)
	}
	// Unknown operations default to blocked.
	require.True(t, model.IsLegalHoldBlocked(model.StateLegalHold, "mystery_op"))
}

// TestAllowedActions is a smoke test on the reverse lookup used by UIs.
func TestAllowedActions(t *testing.T) {
	require.Contains(t, model.AllowedActions(model.StateDraft), model.ActionSubmitForReview)
	require.Contains(t, model.AllowedActions(model.StateDraft), model.ActionApplyHold)
	require.NotContains(t, model.AllowedActions(model.StateDraft), model.ActionApprove)

	require.Contains(t, model.AllowedActions(model.StateInReview), model.ActionApprove)
	require.Contains(t, model.AllowedActions(model.StateInReview), model.ActionReject)
	require.Empty(t, model.AllowedActions(model.StateDisposed), "disposed is terminal")
}

// TestNewOutboxEvent confirms payload marshalling and subject derivation.
func TestNewOutboxEvent(t *testing.T) {
	evt, err := model.NewOutboxEvent(
		mustV7(t), "dms.document.created.v1", "document", mustV7(t),
		model.DocumentCreatedPayload{DocumentID: "x", Title: "t"},
	)
	require.NoError(t, err)
	require.Equal(t, "dms.document.created", evt.Subject)
	require.Contains(t, string(evt.Payload), `"document_id":"x"`)
}

// TestVersionUploadedPayloadShape pins the JSON field names so proto
// (VersionUploadedV1 in events.proto) and wire payload stay in sync.
// Consumers (intelligence, preview, search) are subscribed to
// dms.version.uploaded.v1 with these exact field names — breaking them
// breaks the whole intelligence pipeline.
func TestVersionUploadedPayloadShape(t *testing.T) {
	evt, err := model.NewOutboxEvent(
		mustV7(t), "dms.version.uploaded.v1", "version", mustV7(t),
		model.VersionUploadedPayload{
			EventID:          "evt",
			TenantID:         "ten",
			DocumentID:       "doc",
			VersionID:        "ver",
			VersionNumber:    2,
			ContentBlobID:    "blob",
			StorageURI:       "s3://b/k",
			MimeType:         "application/pdf",
			SizeBytes:        1024,
			SHA256:           "abc",
			UploadedByUserID: "usr",
			UploadedAt:       "2026-04-17T00:00:00Z",
		},
	)
	require.NoError(t, err)
	body := string(evt.Payload)
	for _, key := range []string{
		`"event_id":"evt"`,
		`"tenant_id":"ten"`,
		`"document_id":"doc"`,
		`"version_id":"ver"`,
		`"version_number":2`,
		`"content_blob_id":"blob"`,
		`"storage_uri":"s3://b/k"`,
		`"mime_type":"application/pdf"`,
		`"size_bytes":1024`,
		`"sha256":"abc"`,
		`"uploaded_by_user_id":"usr"`,
		`"uploaded_at":"2026-04-17T00:00:00Z"`,
	} {
		require.Contains(t, body, key, "payload missing %s", key)
	}
}
