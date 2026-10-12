//go:build integration

package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// auditTestBroker starts NATS and binds every subject the audit consumer
// subscribes to into one small test stream, plus the AUDIT_EVENTS_DLQ
// stream (pkg/events.DefaultStreams reserves far more storage than a test
// container has, issue #84).
func auditTestBroker(ctx context.Context, t *testing.T) (*nats.Conn, nats.JetStreamContext) {
	t.Helper()
	url, cleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "TEST_ALL", Subjects: auditSubjects, MaxBytes: 64 << 20})
	require.NoError(t, err)
	_, err = js.AddStream(&nats.StreamConfig{Name: "AUDIT_EVENTS_DLQ", Subjects: []string{"dms.dlq.audit_events.>"}, MaxBytes: 16 << 20})
	require.NoError(t, err)
	return nc, js
}

// O-5: an event the audit consumer cannot ingest used to be NAK'd until
// MaxDeliver and then silently dropped by JetStream — evidence loss with
// nothing left to replay. On the last delivery it must be parked on the
// AUDIT_EVENTS_DLQ stream (30-day retention) and only then acked.
func TestAuditConsumer_PoisonGoesToDLQ(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, js := auditTestBroker(ctx, t)

	svc := New(Config{Logger: zerolog.Nop()})
	svc.maxDeliver = 2
	svc.nakDelays = []time.Duration{20 * time.Millisecond}
	require.NoError(t, svc.StartConsumer(ctx, js))

	poison := []byte(`{not json`)
	_, err := js.Publish("dms.document.poisoned.v1", poison)
	require.NoError(t, err)

	dlq, err := js.SubscribeSync("dms.dlq.audit_events.>", nats.BindStream("AUDIT_EVENTS_DLQ"), nats.DeliverAll())
	require.NoError(t, err)
	msg, err := dlq.NextMsg(30 * time.Second)
	require.NoError(t, err, "the poison event must reach the audit DLQ stream")
	require.Equal(t, "dms.dlq.audit_events.ingest.dms.document.poisoned.v1", msg.Subject)
	var parked struct {
		OriginalSubject string `json:"original_subject"`
		Error           string `json:"error"`
		Delivered       uint64 `json:"delivered"`
		RawB64          []byte `json:"raw_b64"`
	}
	require.NoError(t, json.Unmarshal(msg.Data, &parked))
	require.Equal(t, "dms.document.poisoned.v1", parked.OriginalSubject)
	require.NotEmpty(t, parked.Error)
	require.EqualValues(t, 2, parked.Delivered)
	require.Equal(t, poison, parked.RawB64, "the original bytes must be kept for replay")

	// Parked, then acked: nothing left pending on the source consumer.
	require.Eventually(t, func() bool {
		ci, err := js.ConsumerInfo("TEST_ALL", "audit-dms_document")
		return err == nil && ci.NumAckPending == 0 && ci.NumRedelivered == 0
	}, 10*time.Second, 100*time.Millisecond)
}
