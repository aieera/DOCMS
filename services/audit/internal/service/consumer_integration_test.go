//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
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

// M-10: the durable push consumers had no deliver group, so a second
// replica failed StartConsumer with "consumer is already bound to a
// subscription" and log.Fatal'd — audit could only ever run one pod. Both
// replicas must bind, share the work (each event handled once), survive a
// durable created by an older build without a group, and a replica
// draining on shutdown must not delete the shared durable.
func TestConsumers_TwoInstancesBind(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	nc, js := auditTestBroker(ctx, t)

	// A durable left behind by the pre-fix build: no deliver group.
	legacyNC, err := nats.Connect(nc.ConnectedUrl())
	require.NoError(t, err)
	legacyJS, err := legacyNC.JetStream()
	require.NoError(t, err)
	_, err = legacyJS.Subscribe("dms.document.>", func(*nats.Msg) {}, nats.Durable("audit-dms_document"), nats.ManualAck(),
		nats.MaxDeliver(5), nats.AckWait(30*time.Second))
	require.NoError(t, err)
	legacyNC.Close() // the old pod exits; its durable stays

	newReplica := func() (*Service, nats.JetStreamContext) {
		c, err := nats.Connect(nc.ConnectedUrl())
		require.NoError(t, err)
		t.Cleanup(c.Close)
		j, err := c.JetStream()
		require.NoError(t, err)
		s := New(Config{Logger: zerolog.Nop()})
		s.nakDelays = []time.Duration{5 * time.Millisecond} // poison reaches the DLQ fast
		return s, j
	}
	a, jsA := newReplica()
	b, jsB := newReplica()
	require.NoError(t, a.StartConsumer(ctx, jsA))
	require.NoError(t, b.StartConsumer(ctx, jsB), "the second replica must bind the same durables")

	ci, err := js.ConsumerInfo("TEST_ALL", "audit-dms_document")
	require.NoError(t, err)
	require.Equal(t, "audit-dms_document", ci.Config.DeliverGroup)

	const n = 20
	for i := 0; i < n; i++ {
		_, err := js.Publish("dms.document.poisoned.v1", []byte(`{not json`))
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool {
		si, err := js.StreamInfo("AUDIT_EVENTS_DLQ")
		return err == nil && si.State.Msgs >= n
	}, 30*time.Second, 100*time.Millisecond, "events on the upgraded durable must be delivered")
	time.Sleep(500 * time.Millisecond)
	si, err := js.StreamInfo("AUDIT_EVENTS_DLQ")
	require.NoError(t, err)
	require.EqualValues(t, n, si.State.Msgs, "each event must be handled by exactly one replica")
}

// O-5 follow-up: when the DLQ publish itself failed on the final delivery,
// the consumer NAK'd — but JetStream never redelivers past MaxDeliver, so
// the event was lost anyway. The consumer must keep the message pending
// (InProgress) and retry the DLQ publish, so a DLQ that comes back in time
// still receives it.
func TestAuditConsumer_DLQPublishFailureKeepsMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, js := auditTestBroker(ctx, t)
	require.NoError(t, js.DeleteStream("AUDIT_EVENTS_DLQ")) // the DLQ is unavailable at first

	svc := New(Config{Logger: zerolog.Nop()})
	svc.maxDeliver = 1
	svc.nakDelays = []time.Duration{10 * time.Millisecond}
	svc.dlqRetryDelays = []time.Duration{300 * time.Millisecond}
	svc.dlqRetryAttempts = 40
	require.NoError(t, svc.StartConsumer(ctx, js))

	_, err := js.Publish("dms.document.poisoned.v1", []byte(`{not json`))
	require.NoError(t, err)
	time.Sleep(time.Second) // final (only) delivery happens; DLQ publish fails

	_, err = js.AddStream(&nats.StreamConfig{Name: "AUDIT_EVENTS_DLQ", Subjects: []string{"dms.dlq.audit_events.>"}, MaxBytes: 16 << 20})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		si, err := js.StreamInfo("AUDIT_EVENTS_DLQ")
		return err == nil && si.State.Msgs == 1
	}, 20*time.Second, 100*time.Millisecond, "the event must reach the DLQ once it is back")
	require.Eventually(t, func() bool {
		ci, err := js.ConsumerInfo("TEST_ALL", "audit-dms_document")
		return err == nil && ci.NumAckPending == 0
	}, 10*time.Second, 100*time.Millisecond, "and only then be acked")
}

// O-5 follow-up (H-12 round 2): a DLQ publish that times out after the
// stream stored it is retried by parkOnDLQ; without a Nats-Msg-Id the
// retry stored a second copy. The id is derived from the source stream
// and sequence, so the DLQ stream's duplicate window drops the retry.
func TestAuditDLQ_PublishIsDeduplicated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, js := auditTestBroker(ctx, t)

	_, err := js.Publish("dms.document.poisoned.v1", []byte(`{not json`))
	require.NoError(t, err)
	sub, err := js.SubscribeSync("dms.document.>", nats.BindStream("TEST_ALL"), nats.DeliverAll())
	require.NoError(t, err)
	msg, err := sub.NextMsg(5 * time.Second)
	require.NoError(t, err)
	meta, err := msg.Metadata()
	require.NoError(t, err)

	svc := New(Config{Logger: zerolog.Nop()})
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.toDLQ(js, msg, meta, errors.New("boom")))
	}
	si, err := js.StreamInfo("AUDIT_EVENTS_DLQ")
	require.NoError(t, err)
	require.EqualValues(t, 1, si.State.Msgs, "a retried DLQ publish must not store a duplicate")
}
