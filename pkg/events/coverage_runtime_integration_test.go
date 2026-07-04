//go:build integration
// +build integration

// Runtime counterpart to the build-time gate in coverage_test.go. The unit
// test proves DefaultStreams *statically* covers PublishedSubjects; this
// proves the *live* guard AssertLiveCoverage (wired into ConnectNATS, §4)
// refuses a drifted topology where a subject's stream is missing.
//
// We provision a single tiny stream directly rather than the full
// DefaultStreams set: that set's summed MaxBytes (~240 GB of caps) exceeds
// the default test container's JetStream file store ("insufficient storage
// resources") — a harness sizing limit unrelated to the assertion logic,
// and one real/compose NATS (adequate max_file_store) does not hit.
//
// Run with: go test -tags integration ./pkg/events/...
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

func TestAssertLiveCoverage_RuntimeGuard(t *testing.T) {
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

	specs := []events.StreamSpec{
		{Name: "USER_EVENTS", Subjects: []string{"dms.user.>", "dms.session.>", "dms.apikey.>", "dms.auth.>"}},
	}
	// Two representative published subjects bound by USER_EVENTS.
	subjects := []string{"dms.user.invited.v1", "dms.apikey.rotated.v1"}

	mkStream := func() {
		_, e := js.AddStream(&nats.StreamConfig{
			Name:     "USER_EVENTS",
			Subjects: specs[0].Subjects,
			Storage:  nats.FileStorage,
			MaxBytes: 1 << 20, // 1 MiB — keep independent of container store size
		})
		require.NoError(t, e)
	}

	// Green when the stream is live.
	mkStream()
	require.NoError(t, events.AssertLiveCoverage(js, subjects, specs),
		"clean topology must satisfy live coverage")

	// Operational drift: the stream is deleted out-of-band. Its subjects now
	// have no home — exactly the silent-loss condition §4 guards against.
	require.NoError(t, js.DeleteStream("USER_EVENTS"))
	err = events.AssertLiveCoverage(js, subjects, specs)
	require.Error(t, err, "guard must fail when a subject's stream is gone")
	require.Contains(t, err.Error(), "silently dropped")
	require.Contains(t, err.Error(), "dms.user.invited.v1")

	// Reprovision → coverage restored (the guard is not sticky).
	mkStream()
	require.NoError(t, events.AssertLiveCoverage(js, subjects, specs))
}
