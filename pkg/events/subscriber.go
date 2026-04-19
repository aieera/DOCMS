package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Handler processes a message. Returning nil acks; returning a non-nil error
// nacks for redelivery according to the consumer's backoff policy.
type Handler func(ctx context.Context, msg *nats.Msg) error

// Subscriber creates durable JetStream consumers with manual ack.
type Subscriber struct {
	js nats.JetStreamContext
}

// NewSubscriber constructs a Subscriber.
func NewSubscriber(js nats.JetStreamContext) *Subscriber {
	return &Subscriber{js: js}
}

// Subscribe registers a durable consumer. stream is the JetStream stream
// name; subject is the filter (can contain wildcards); consumerName is the
// durable name (typically "<service>-<purpose>"). Subscribe blocks briefly
// to register, then returns; messages are delivered to handler in goroutines.
//
// AckWait=30s, MaxDeliver=5, DeliverPolicy=DeliverAll on first creation.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	stream, subject, consumerName string,
	handler Handler,
) (*nats.Subscription, error) {
	sub, err := s.js.PullSubscribe(subject, consumerName,
		nats.BindStream(stream),
		nats.ManualAck(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(5),
		nats.DeliverAll(),
	)
	if err != nil {
		return nil, fmt.Errorf("pull subscribe: %w", err)
	}

	go s.consume(ctx, sub, handler)
	return sub, nil
}

func (s *Subscriber) consume(ctx context.Context, sub *nats.Subscription, handler Handler) {
	for {
		select {
		case <-ctx.Done():
			_ = sub.Drain()
			return
		default:
		}
		msgs, err := sub.Fetch(16, nats.MaxWait(5*time.Second))
		if err != nil {
			if err == nats.ErrTimeout {
				continue
			}
			time.Sleep(time.Second)
			continue
		}
		for _, m := range msgs {
			s.handleOne(ctx, m, handler)
		}
	}
}

func (s *Subscriber) handleOne(ctx context.Context, m *nats.Msg, handler Handler) {
	hctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := handler(hctx, m); err != nil {
		_ = m.NakWithDelay(backoff(m))
		return
	}
	_ = m.Ack()
}

func backoff(m *nats.Msg) time.Duration {
	md, err := m.Metadata()
	if err != nil {
		return 5 * time.Second
	}
	// Exponential-ish: 1s, 2s, 8s, 32s, 128s capped
	switch md.NumDelivered {
	case 1:
		return time.Second
	case 2:
		return 2 * time.Second
	case 3:
		return 8 * time.Second
	case 4:
		return 32 * time.Second
	default:
		return 128 * time.Second
	}
}
