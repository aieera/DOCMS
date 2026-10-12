// Package dlq parks audit events the consumer gave up on (O-5) on the
// AUDIT_EVENTS_DLQ stream.
//
// It republishes JetStream→JetStream with no database commit in between,
// so the transactional outbox does not apply; the source message stays
// pending until the park is stored, which gives the same at-least-once
// guarantee. pkg/archtest C5 whitelists this package by path for exactly
// that reason, so it must stay free of database access: it imports nats
// only.
package dlq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Prefix is where events the consumer gave up on are parked. It is bound
// to the AUDIT_EVENTS_DLQ stream (pkg/events creates it beside
// AUDIT_EVENTS with 30-day retention); the original subject is appended.
const Prefix = "dms.dlq.audit_events.ingest."

// Publish sends a permanently failed event to the audit DLQ stream and
// waits for the stream to store it. The original bytes travel base64 so a
// non-JSON poison message survives intact for replay.
func Publish(js nats.JetStreamContext, msg *nats.Msg, meta *nats.MsgMetadata, cause error) error {
	payload, err := json.Marshal(map[string]any{
		"original_subject": msg.Subject,
		"error":            cause.Error(),
		"delivered":        meta.NumDelivered,
		"failed_at":        time.Now().UTC().Format(time.RFC3339Nano),
		"raw_b64":          msg.Data,
	})
	if err != nil {
		return err
	}
	out := nats.NewMsg(Prefix + msg.Subject)
	out.Data = payload
	// Nats-Msg-Id from the source stream position: a publish that timed
	// out after the DLQ stored it is retried by Park, and the DLQ stream's
	// duplicate window drops the second copy.
	out.Header.Set(nats.MsgIdHdr, fmt.Sprintf("audit-dlq:%s:%d:%s", meta.Stream, meta.Sequence.Stream, msg.Subject))
	_, err = js.PublishMsg(out)
	return err
}

// Retry is the DLQ publish retry policy on the final delivery.
type Retry struct {
	Attempts int
	Delays   []time.Duration // the last one repeats; must be non-empty when Attempts > 1
	// OnFailure, when set, is told about each failed attempt (1-based).
	OnFailure func(attempt int, err error)
}

// Park publishes a final-delivery failure to the DLQ, retrying a failed
// publish (DLQ stream briefly unavailable, NATS blip) while
// msg.InProgress() keeps the message pending: this is the last delivery,
// so once it is NAK'd or its AckWait lapses JetStream drops it for good.
// It returns the last publish error, or a shutdown error when ctx ends.
func Park(ctx context.Context, js nats.JetStreamContext, msg *nats.Msg, meta *nats.MsgMetadata, cause error, r Retry) error {
	var err error
	for attempt := 0; attempt < r.Attempts; attempt++ {
		if err = Publish(js, msg, meta, cause); err == nil {
			return nil
		}
		if r.OnFailure != nil {
			r.OnFailure(attempt+1, err)
		}
		_ = msg.InProgress()
		delay := r.Delays[min(attempt, len(r.Delays)-1)]
		select {
		case <-ctx.Done():
			return fmt.Errorf("shutting down: %w", err)
		case <-time.After(delay):
		}
		_ = msg.InProgress()
	}
	return err
}
