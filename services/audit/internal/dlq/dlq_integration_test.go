//go:build integration

package dlq

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

func testBroker(ctx context.Context, t *testing.T) nats.JetStreamContext {
	t.Helper()
	url, cleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "TEST_ALL", Subjects: []string{"dms.document.>"}, MaxBytes: 16 << 20})
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "AUDIT_EVENTS_DLQ", Subjects: []string{"dms.dlq.audit_events.>"}, MaxBytes: 16 << 20})
	require.NoError(t, err)
	return js
}

// O-5 follow-up (H-12 round 2): a DLQ publish that times out after the
// stream stored it is retried by Park; without a Nats-Msg-Id the retry
// stored a second copy. The id is derived from the source stream and
// sequence, so the DLQ stream's duplicate window drops the retry.
func TestAuditDLQ_PublishIsDeduplicated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	js := testBroker(ctx, t)

	_, err := js.Publish("dms.document.poisoned.v1", []byte(`{not json`))
	require.NoError(t, err)
	sub, err := js.SubscribeSync("dms.document.>", nats.BindStream("TEST_ALL"), nats.DeliverAll())
	require.NoError(t, err)
	msg, err := sub.NextMsg(5 * time.Second)
	require.NoError(t, err)
	meta, err := msg.Metadata()
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		require.NoError(t, Publish(js, msg, meta, errors.New("boom")))
	}
	si, err := js.StreamInfo("AUDIT_EVENTS_DLQ")
	require.NoError(t, err)
	require.EqualValues(t, 1, si.State.Msgs, "a retried DLQ publish must not store a duplicate")
}
