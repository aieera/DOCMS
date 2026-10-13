//go:build integration
// +build integration

package events_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/testutil"
)

// TestEnsureStreams_AuditDiscardNewOnRealBroker (O-5): a broker provisioned
// before the change has AUDIT_EVENTS on DiscardOld with the same subjects.
// EnsureStreams must detect the discard-policy drift and the broker must
// accept the in-place update — every service runs EnsureStreams at boot,
// so a rejected update would stop them all from starting.
func TestEnsureStreams_AuditDiscardNewOnRealBroker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	url, cleanup, err := testutil.NewNATSContainer(ctx)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	nc, err := nats.Connect(url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	var audit events.StreamSpec
	for _, s := range events.DefaultStreams {
		if s.Name == "AUDIT_EVENTS" {
			audit = s
		}
	}
	require.True(t, audit.DiscardNew)

	// The pre-O-5 stream: same subjects, DiscardOld.
	_, err = js.AddStream(&nats.StreamConfig{
		Name: audit.Name, Subjects: audit.Subjects, Storage: nats.FileStorage,
		Retention: nats.LimitsPolicy, Discard: nats.DiscardOld, MaxAge: 168 * time.Hour,
	})
	require.NoError(t, err)

	report, err := events.EnsureStreams(js, []events.StreamSpec{audit})
	require.NoError(t, err)
	require.Contains(t, report.Updated, audit.Name)

	info, err := js.StreamInfo(audit.Name)
	require.NoError(t, err)
	require.Equal(t, nats.DiscardNew, info.Config.Discard)

	// Idempotent: a second boot sees no drift.
	report, err = events.EnsureStreams(js, []events.StreamSpec{audit})
	require.NoError(t, err)
	require.Contains(t, report.Skipped, audit.Name)
}
