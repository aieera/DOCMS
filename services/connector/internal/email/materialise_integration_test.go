//go:build integration && prodposture
// +build integration,prodposture

package email

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// N-8: an attachment that fails to ingest fails the whole message (it is
// retried, not silently dropped), and the retry resumes per part — the
// body and attachments already filed are not ingested again.
func TestEmailMaterialise_AttachmentFailureRetries(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	fx := newEmailFixture(ctx, t, SourceGmail)

	env := &Envelope{
		SourceMessageID: "gmail/att-1", Subject: "Quarterly report", Date: time.Now(),
		Attachments: []Attachment{
			{Filename: "a.pdf", ContentType: "application/pdf", Bytes: []byte("%PDF a")},
			{Filename: "", ContentType: "application/octet-stream", Bytes: []byte("unnamed")},
		},
	}
	ing := newCountingIngester()
	ing.failNames["a.pdf"] = true
	svc := newTestService(fx, &staticPoller{src: SourceGmail, envs: []*Envelope{env}}, ing)

	_, err := svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)
	require.Equal(t, "failed", fx.statuses(ctx, t)["gmail/att-1"], "an attachment failure must fail the message")
	require.Equal(t, 1, ing.count("Quarterly report.md"))
	require.Equal(t, 1, ing.count("a.pdf"))

	// Cause fixed; the next poll retries and resumes.
	ing.mu.Lock()
	ing.failNames["a.pdf"] = false
	ing.mu.Unlock()
	_, err = svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)
	require.Equal(t, "materialised", fx.statuses(ctx, t)["gmail/att-1"])
	require.Equal(t, 1, ing.count("Quarterly report.md"), "body must not be re-ingested on retry")
	require.Equal(t, 2, ing.count("a.pdf"), "failed attachment retried once")
	require.Equal(t, 1, ing.count("attachment-2"), "unnamed attachment gets a default filename")

	var docID string
	var attach []byte
	require.NoError(t, database.WithTenantTx(ctx, fx.db.App, fx.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(document_id::text, ''), array_to_json(attachment_document_ids)::text
			FROM email_messages WHERE tenant_id = $1 AND source_message_id = 'gmail/att-1'`, fx.tenant).Scan(&docID, &attach)
	}))
	require.NotEmpty(t, docID)
	var ids []*string
	require.NoError(t, json.Unmarshal(attach, &ids))
	require.Len(t, ids, 2)
	for i, id := range ids {
		require.NotNil(t, id, "attachment %d has no document id", i+1)
	}

	// Materialised: a further poll files nothing again.
	_, err = svc.RunOnce(ctx, svc.testTenant, svc.testConfig)
	require.NoError(t, err)
	require.Equal(t, 1, ing.count("Quarterly report.md"))
	require.Equal(t, 2, ing.count("a.pdf"))
	require.Equal(t, 1, ing.count("attachment-2"))
}
