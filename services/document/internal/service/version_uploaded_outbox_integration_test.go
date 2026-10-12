//go:build integration

// CreateVersion must write the canonical dms.version.uploaded.v1 outbox row
// (ADR 0021) — the intelligence pipeline's only trigger — in the same tx as
// the version. TestCreateVersion_PublishesVersionUploadedEventViaOutbox
// covers the NATS hop but cannot run where the default JetStream stream
// reservations (~240 GB, issue #84) exceed the host's disk; this pins the
// outbox half of the contract on the rewritten link path (blob read inside
// the tenant tx, DEC-12 claim) without NATS.
package service_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/services/document/internal/model"
)

func TestCreateVersion_WritesVersionUploadedOutboxRow(t *testing.T) {
	h := newHarness(t, true)
	wsID := uuid.Must(uuid.NewV7())
	f := mustCreateFolder(t, h, wsID, "Events")
	doc := mustDocIn(t, h, h.svc, wsID, f.ID, "evented")
	blob := seedUploadedBlob(t, h, h.user, "us-east-1")
	v := mustLinkVersion(t, h, h.svc, doc.ID, blob) // caller passes only the blob id

	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM outbox
		 WHERE tenant_id = $1 AND event_type = 'dms.version.uploaded.v1'
		   AND aggregate_type = 'version' AND aggregate_id = $2`, h.tenant, v.ID),
		"exactly one version.uploaded row per version")
	var raw []byte
	require.NoError(t, h.pool.QueryRow(h.ctx, `
		SELECT payload FROM outbox
		 WHERE tenant_id = $1 AND event_type = 'dms.version.uploaded.v1'
		   AND aggregate_type = 'version' AND aggregate_id = $2`, h.tenant, v.ID).Scan(&raw))
	var p model.VersionUploadedPayload
	require.NoError(t, json.Unmarshal(raw, &p))

	require.NotEmpty(t, p.EventID)
	require.Equal(t, h.tenant.String(), p.TenantID)
	require.Equal(t, doc.ID.String(), p.DocumentID)
	require.Equal(t, v.ID.String(), p.VersionID)
	require.Equal(t, 1, p.VersionNumber)
	require.Equal(t, blob.ID.String(), p.ContentBlobID)
	require.Equal(t, "s3://"+blob.Bucket+"/"+blob.Key, p.StorageURI)
	require.Equal(t, int64(1024), p.SizeBytes, "size comes from the blob row")
	require.Equal(t, blob.SHA, p.SHA256)
	require.Equal(t, "application/pdf", p.MimeType)
	require.Equal(t, h.user.String(), p.UploadedByUserID)
	_, err := time.Parse(time.RFC3339, p.UploadedAt)
	require.NoError(t, err)

	require.Equal(t, 1, countRows(t, h, `
		SELECT count(*) FROM outbox
		 WHERE tenant_id = $1 AND event_type = 'dms.notify.document.uploaded.v1' AND aggregate_id = $2`,
		h.tenant, doc.ID), "the uploader's inbox notify is dual-written")
}
