//go:build integration

package service

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// M-10 (policy): the policy-perm-cache-invalidator durable had no deliver
// group, so on the second policy replica Start failed with "consumer is
// already bound" (logged, and cache freshness fell back to the 60s TTL),
// and the first replica's Stop deleted the durable. Both replicas must
// bind, and one replica stopping must leave the durable in place.
func TestConsumers_TwoInstancesBind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	url, cleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	connect := func() nats.JetStreamContext {
		nc, err := nats.Connect(url)
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		js, err := nc.JetStream()
		require.NoError(t, err)
		return js
	}
	js := connect()
	_, err = js.AddStream(&nats.StreamConfig{Name: "POLICY_EVENTS", MaxBytes: 16 << 20,
		Subjects: []string{"dms.policy.>", "dms.permission.>"}})
	require.NoError(t, err)

	c, _, _ := newTestCache(t)
	a := NewPermissionCacheInvalidator(connect(), c, zerolog.Nop())
	b := NewPermissionCacheInvalidator(connect(), c, zerolog.Nop())
	require.NoError(t, a.Start(ctx))
	require.NoError(t, b.Start(ctx), "the second replica must bind the same durable")

	a.Stop()
	time.Sleep(300 * time.Millisecond)
	ci, err := js.ConsumerInfo("POLICY_EVENTS", "policy-perm-cache-invalidator")
	require.NoError(t, err, "a replica's shutdown must not delete the shared durable")
	require.Equal(t, "policy-perm-cache-invalidator", ci.Config.DeliverGroup)
}
