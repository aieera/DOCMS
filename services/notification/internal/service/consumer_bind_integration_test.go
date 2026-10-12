//go:build integration

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
	"github.com/aieera/sedoc/services/notification/internal/service"
)

// M-10: notification-all and notification-send had no deliver group, so the
// second notification replica failed StartConsumer with "consumer is already
// bound to a subscription" and log.Fatal'd. Both replicas must bind, also
// over the durable the previous build left behind.
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
	_, err = js.AddStream(&nats.StreamConfig{Name: "NOTIFY_EVENTS", MaxBytes: 16 << 20,
		Subjects: []string{"dms.notify.>", "dms.notification.>"}})
	require.NoError(t, err)

	legacyNC, err := nats.Connect(url)
	require.NoError(t, err)
	legacyJS, err := legacyNC.JetStream()
	require.NoError(t, err)
	_, err = legacyJS.Subscribe("dms.notify.>", func(*nats.Msg) {},
		nats.Durable("notification-all"), nats.ManualAck(), nats.MaxDeliver(5))
	require.NoError(t, err)
	legacyNC.Close()

	for i := 0; i < 2; i++ {
		svc := service.New(service.Config{Logger: zerolog.Nop()})
		jsi := connect()
		require.NoError(t, svc.StartConsumer(ctx, jsi), "replica %d: dms.notify.>", i)
		require.NoError(t, svc.StartNotificationSendConsumer(ctx, jsi), "replica %d: notification.send", i)
	}
	for _, d := range []string{"notification-all", "notification-send"} {
		ci, err := js.ConsumerInfo("NOTIFY_EVENTS", d)
		require.NoError(t, err)
		require.Equal(t, d, ci.Config.DeliverGroup)
	}
}
