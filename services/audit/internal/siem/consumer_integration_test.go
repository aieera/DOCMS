//go:build integration

package siem

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// M-10: the siem-* durables had no deliver group either, so a second audit
// replica could not start SIEM forwarding.
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
	_, err = js.AddStream(&nats.StreamConfig{Name: "TEST_ALL", Subjects: siemSubjects, MaxBytes: 16 << 20})
	require.NoError(t, err)

	require.NoError(t, New(nil, zerolog.Nop()).StartConsumer(ctx, connect()))
	require.NoError(t, New(nil, zerolog.Nop()).StartConsumer(ctx, connect()),
		"a second replica must bind the siem-* durables")
	ci, err := js.ConsumerInfo("TEST_ALL", "siem-dms_document")
	require.NoError(t, err)
	require.Equal(t, "siem-dms_document", ci.Config.DeliverGroup)
}
