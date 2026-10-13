// Package c5fixture is a synthetic violation set for the type-aware C5
// guard (TestPhaseC5_TypeAwareGuardCatchesFixture). It is never built into
// anything; testdata/ is invisible to ./... patterns.
package c5fixture

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type bus struct{ conn *nats.Conn }

type server struct {
	bus bus
	js  nats.JetStreamContext
	njs jetstream.JetStream
}

// embedsConn promotes *nats.Conn's methods.
type embedsConn struct{ *nats.Conn }

// redisLike has a Publish method of its own and must NOT be flagged.
type redisLike struct{}

func (redisLike) Publish(string, []byte) error { return nil }

func violations(ctx context.Context, conn *nats.Conn, s *server, e embedsConn, r redisLike) {
	_ = conn.PublishMsg(&nats.Msg{Subject: "a"}) // VIOLATION conn.PublishMsg
	_ = s.bus.conn.Publish("a", nil)             // VIOLATION field chain
	_, _ = s.js.PublishAsync("a", nil)           // VIOLATION JetStreamContext
	_, _ = s.njs.Publish(ctx, "a", nil)          // VIOLATION jetstream.JetStream
	_ = e.Publish("a", nil)                      // VIOLATION promoted via embedding
	pub := conn.Publish                          // VIOLATION method value
	_ = pub
	_ = r.Publish("a", nil) // ok: not a NATS receiver
}
