//go:build integration && prodposture
// +build integration,prodposture

// N-4: with two connector replicas, every webhook delivery must reach the
// receiver exactly once. Schema of record (document 000001 tables 37-38)
// plus the connector's real migrations, under NOBYPASSRLS.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/connector/internal/model"
	"github.com/aieera/sedoc/services/connector/internal/repository"
)

func TestWebhookDelivery_TwoWorkersDeliverOnce(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	db := testutil.NewProdPostureDB(ctx, t, "")
	_, err := db.Super.Exec(ctx, `
		CREATE TABLE organizations (id UUID PRIMARY KEY, deleted_at TIMESTAMPTZ);
		CREATE TABLE users (tenant_id UUID NOT NULL REFERENCES organizations(id), id UUID NOT NULL, PRIMARY KEY (tenant_id, id));
		CREATE TABLE webhook_subscriptions (
			tenant_id UUID NOT NULL REFERENCES organizations(id),
			id UUID NOT NULL DEFAULT gen_random_uuid(),
			url TEXT NOT NULL, events TEXT[] NOT NULL, secret TEXT NOT NULL,
			is_active BOOLEAN NOT NULL DEFAULT true,
			failure_count INT NOT NULL DEFAULT 0,
			last_success_at TIMESTAMPTZ, last_failure_at TIMESTAMPTZ,
			created_by UUID,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, created_by) REFERENCES users(tenant_id, id)
		);
		ALTER TABLE webhook_subscriptions ENABLE ROW LEVEL SECURITY;
		ALTER TABLE webhook_subscriptions FORCE  ROW LEVEL SECURITY;
		CREATE POLICY ws_iso ON webhook_subscriptions USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY ws_ins ON webhook_subscriptions FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE TABLE webhook_deliveries (
			tenant_id UUID NOT NULL REFERENCES organizations(id),
			id UUID NOT NULL DEFAULT gen_random_uuid(),
			subscription_id UUID NOT NULL, event_type TEXT NOT NULL, event_id TEXT NOT NULL,
			payload JSONB NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed', 'dead_letter')),
			http_status INT, attempts INT NOT NULL DEFAULT 0,
			last_attempt_at TIMESTAMPTZ, next_retry_at TIMESTAMPTZ, error_message TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, id),
			FOREIGN KEY (tenant_id, subscription_id) REFERENCES webhook_subscriptions(tenant_id, id)
		);
		ALTER TABLE webhook_deliveries ENABLE ROW LEVEL SECURITY;
		ALTER TABLE webhook_deliveries FORCE  ROW LEVEL SECURITY;
		CREATE POLICY wd_iso ON webhook_deliveries USING (tenant_id = current_setting('app.current_tenant', true)::uuid);
		CREATE POLICY wd_ins ON webhook_deliveries FOR INSERT WITH CHECK (tenant_id = current_setting('app.current_tenant', true)::uuid);
		GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_subscriptions, webhook_deliveries, users, organizations TO dms_app;
	`)
	require.NoError(t, err)
	require.NoError(t, database.RunMigrations(db.SuperDSN, "../../migrations"))

	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct {
			N string `json:"n"`
		}
		_ = json.Unmarshal(body, &p)
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		hits[p.N]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	tenant := uuid.New()
	creator := uuid.New()
	_, err = db.Super.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1); `, tenant)
	require.NoError(t, err)
	_, err = db.Super.Exec(ctx, `INSERT INTO users (tenant_id, id) VALUES ($1, $2)`, tenant, creator)
	require.NoError(t, err)
	repo := repository.New(db.App)
	sub := &model.WebhookSubscription{ID: uuid.NewString(), TenantID: tenant.String(), URL: srv.URL,
		Secret: "whsec_x", Events: []string{"dms.document.created.v1"}, Active: true,
		CreatedBy: creator.String(), CreatedAt: time.Now()}
	require.NoError(t, repo.CreateWebhook(ctx, sub))
	const n = 30
	for i := 0; i < n; i++ {
		require.NoError(t, repo.InsertDelivery(ctx, &model.WebhookDelivery{
			ID: uuid.NewString(), SubscriptionID: sub.ID, TenantID: tenant.String(),
			EventType: "dms.document.created.v1", Payload: []byte(fmt.Sprintf(`{"n":"%d"}`, i)),
			CreatedAt: time.Now().Add(time.Duration(i) * time.Millisecond),
		}))
	}

	w1 := NewDeliveryWorker(repo, zerolog.Nop(), true)
	w2 := NewDeliveryWorker(repo, zerolog.Nop(), true)
	var wg sync.WaitGroup
	for _, w := range []*DeliveryWorker{w1, w2} {
		wg.Add(1)
		go func(w *DeliveryWorker) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				w.processBatch(ctx)
			}
		}(w)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		require.Equal(t, 1, hits[fmt.Sprint(i)], "delivery %d sent %d times", i, hits[fmt.Sprint(i)])
	}
	var delivered int
	require.NoError(t, database.WithTenantTx(ctx, db.App, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE status = 'delivered' AND next_retry_at IS NULL`).Scan(&delivered)
	}))
	require.Equal(t, n, delivered)
}
