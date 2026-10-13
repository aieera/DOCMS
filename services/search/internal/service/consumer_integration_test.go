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

// M-10: the indexer's durable push consumers had no deliver group, so the
// second search replica failed indexer.Start with "consumer is already
// bound to a subscription" and log.Fatal'd. Both replicas must bind (also
// over a durable left by the previous build), and one replica's Stop
// (Drain) must not delete the durable the other is using.
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
	_, err = js.AddStream(&nats.StreamConfig{Name: "TEST_ALL", MaxBytes: 16 << 20,
		Subjects: []string{"dms.document.>", "dms.version.>", "dms.permission.>", "dms.folder.>"}})
	require.NoError(t, err)

	legacyNC, err := nats.Connect(url)
	require.NoError(t, err)
	legacyJS, err := legacyNC.JetStream()
	require.NoError(t, err)
	_, err = legacyJS.Subscribe("dms.document.created.v1", func(*nats.Msg) {},
		nats.Durable("search-dms_document_created_v1"), nats.ManualAck(), nats.MaxDeliver(5), nats.AckWait(60*time.Second))
	require.NoError(t, err)
	legacyNC.Close()

	a := NewIndexer(nil, nil, connect(), zerolog.Nop())
	b := NewIndexer(nil, nil, connect(), zerolog.Nop())
	require.NoError(t, a.Start(ctx))
	require.NoError(t, b.Start(ctx), "the second replica must bind the same durables")

	a.Stop()
	time.Sleep(300 * time.Millisecond)
	for _, d := range []string{"search-dms_document_created_v1", "search-dms_version_uploaded_v1"} {
		ci, err := js.ConsumerInfo("TEST_ALL", d)
		require.NoError(t, err, "a replica's shutdown must not delete durable %s", d)
		require.Equal(t, d, ci.Config.DeliverGroup)
	}
}
