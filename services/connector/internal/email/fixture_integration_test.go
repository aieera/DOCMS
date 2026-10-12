//go:build integration && prodposture
// +build integration,prodposture

package email

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// emailFixture is a NOBYPASSRLS database with the schema of record
// (document migration 000041) and one active config.
type emailFixture struct {
	db     *testutil.ProdPostureDB
	tenant uuid.UUID
	cfgID  uuid.UUID
}

func newEmailFixture(ctx context.Context, t *testing.T, source Source) *emailFixture {
	t.Helper()
	db := testutil.NewProdPostureDB(ctx, t, "")
	ddl, err := os.ReadFile("../../../document/migrations/000041_email_ingestion.up.sql")
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
		GRANT SELECT ON organizations TO dms_app;`)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, string(ddl))
	require.NoError(t, err, "document 000041 (schema of record) must apply")

	fx := &emailFixture{db: db, tenant: uuid.New(), cfgID: uuid.New()}
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, fx.tenant)
	require.NoError(t, err)
	require.NoError(t, database.WithTenantTx(ctx, db.App, fx.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO email_ingestion_configs
			    (tenant_id, id, source, label, oauth_provider, imap_host, imap_port, imap_username,
			     target_workspace_id, target_folder_id, created_by)
			VALUES ($1, $2, $3, 'inbox', 'google_workspace', 'imap.example.test', 993, 'u',
			        $4, $5, $6)`,
			fx.tenant, fx.cfgID, string(source), uuid.New(), uuid.New(), uuid.New())
		return err
	}))
	return fx
}

func (fx *emailFixture) statuses(ctx context.Context, t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, database.WithTenantTx(ctx, fx.db.App, fx.tenant, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT source_message_id, ingest_status FROM email_messages WHERE tenant_id = $1`, fx.tenant)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, st string
			if err := rows.Scan(&id, &st); err != nil {
				return err
			}
			out[id] = st
		}
		return rows.Err()
	}))
	return out
}

// countingIngester records every IngestFile by filename; failNames fail.
type countingIngester struct {
	mu        sync.Mutex
	delay     time.Duration
	calls     map[string]int
	failNames map[string]bool
}

func newCountingIngester() *countingIngester {
	return &countingIngester{calls: map[string]int{}, failNames: map[string]bool{}}
}

func (c *countingIngester) IngestFile(_ context.Context, _, _, _, _, _ string, filename, _ string, _ []byte, _ map[string]any) (string, error) {
	time.Sleep(c.delay)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[filename]++
	if c.failNames[filename] {
		return "", errFake
	}
	return uuid.NewString(), nil
}

func (c *countingIngester) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errFake = fakeErr("ingest blip")

// staticPoller returns the same envelopes on every poll and counts polls.
type staticPoller struct {
	mu    sync.Mutex
	src   Source
	envs  []*Envelope
	polls int
}

func (p *staticPoller) Source() Source { return p.src }
func (p *staticPoller) Poll(context.Context, *Config) ([]*Envelope, error) {
	p.mu.Lock()
	p.polls++
	p.mu.Unlock()
	return p.envs, nil
}

func newTestService(fx *emailFixture, p Poller, ing ingester) *Service {
	s := New(fx.db.App, nil, nil, []Poller{p}, zerolog.Nop())
	s.ingest = ing
	return s
}
