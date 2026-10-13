// Package jsconsumer binds durable JetStream push consumers so that every
// replica of a service shares one consumer through a deliver group (M-10).
//
// A durable push consumer without a deliver group admits one subscriber:
// the second replica got "consumer is already bound to a subscription" and
// the service log.Fatal'd, so a Deployment with replicas > 1 crash-looped
// and rolling updates stalled. With the durable name as deliver group every
// replica binds and JetStream hands each message to one of them.
//
// The consumer is created (or upgraded) explicitly and the subscription
// binds to it, so nats.go never treats the consumer as its own: Drain or
// Unsubscribe on one replica's shutdown no longer deletes the durable that
// the other replicas — and the next start — depend on.
package jsconsumer

import (
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Options is the delivery policy of the durable.
type Options struct {
	MaxDeliver int
	AckWait    time.Duration
}

// QueueSubscribe binds cb to the durable push consumer `durable` filtered
// on subj, with `durable` as the deliver group and manual acks.
//
// A missing consumer is created. One created by an older build without a
// deliver group is recreated from its ack floor (see ensure). A different
// MaxDeliver/AckWait is updated in place.
func QueueSubscribe(js nats.JetStreamContext, subj, durable string, o Options, cb nats.MsgHandler) (*nats.Subscription, error) {
	stream, err := js.StreamNameBySubject(subj)
	if err != nil {
		return nil, fmt.Errorf("stream for %s: %w", subj, err)
	}
	if err := ensure(js, stream, subj, durable, o); err != nil {
		return nil, fmt.Errorf("consumer %s/%s: %w", stream, durable, err)
	}
	return js.QueueSubscribe(subj, durable, cb, nats.Bind(stream, durable), nats.ManualAck())
}

func ensure(js nats.JetStreamContext, stream, subj, durable string, o Options) error {
	info, err := js.ConsumerInfo(stream, durable)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return create(js, stream, subj, durable, o, 0)
	}
	if err != nil {
		return err
	}
	cfg := info.Config
	if cfg.DeliverSubject == "" {
		return fmt.Errorf("durable %q is a pull consumer", durable)
	}
	if cfg.DeliverGroup != durable {
		// A durable from the pre-M-10 build. nats-server 2.10 accepts an
		// in-place update that adds a deliver group but keeps routing to
		// the old subscription interest, so nothing is ever delivered
		// again. Recreate it instead, resuming after its ack floor:
		// everything it had not acknowledged is redelivered
		// (at-least-once), nothing is skipped.
		if delErr := js.DeleteConsumer(stream, durable); delErr != nil && !errors.Is(delErr, nats.ErrConsumerNotFound) {
			return fmt.Errorf("replace group-less durable: %w", delErr)
		}
		return create(js, stream, subj, durable, o, info.AckFloor.Stream+1)
	}
	if cfg.MaxDeliver == o.MaxDeliver && cfg.AckWait == o.AckWait {
		return nil
	}
	cfg.MaxDeliver = o.MaxDeliver
	cfg.AckWait = o.AckWait
	_, err = js.UpdateConsumer(stream, &cfg)
	return err
}

// create adds the durable with its deliver group. startSeq > 0 resumes at
// that stream sequence; 0 delivers everything the stream holds.
func create(js nats.JetStreamContext, stream, subj, durable string, o Options, startSeq uint64) error {
	cfg := &nats.ConsumerConfig{
		Durable:        durable,
		DeliverSubject: nats.NewInbox(),
		DeliverGroup:   durable,
		DeliverPolicy:  nats.DeliverAllPolicy,
		AckPolicy:      nats.AckExplicitPolicy,
		AckWait:        o.AckWait,
		MaxDeliver:     o.MaxDeliver,
		FilterSubject:  subj,
		ReplayPolicy:   nats.ReplayInstantPolicy,
	}
	if startSeq > 0 {
		cfg.DeliverPolicy = nats.DeliverByStartSequencePolicy
		cfg.OptStartSeq = startSeq
	}
	_, err := js.AddConsumer(stream, cfg)
	var apiErr *nats.APIError
	if errors.As(err, &apiErr) &&
		(apiErr.ErrorCode == nats.JSErrCodeConsumerAlreadyExists || apiErr.ErrorCode == nats.JSErrCodeConsumerNameExists) {
		// Another replica created it first with its own inbox; bind to
		// theirs.
		return nil
	}
	return err
}
