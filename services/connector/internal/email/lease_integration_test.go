//go:build integration && prodposture
// +build integration,prodposture

package email

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/aieera/sedoc/pkg/testutil"
)

// N-4: two connector replicas tick at the same time. The config must be
// polled by one of them and every message materialised exactly once.
func TestEmailPoll_TwoWorkersIngestOnce(t *testing.T) {
	testutil.AssertProdPosture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	fx := newEmailFixture(ctx, t, SourceGmail)

	var envs []*Envelope
	for i := 0; i < 5; i++ {
		envs = append(envs, &Envelope{SourceMessageID: fmt.Sprintf("gmail/%d", i), Subject: fmt.Sprintf("msg-%d", i), Date: time.Now()})
	}
	ing := newCountingIngester()
	ing.delay = 100 * time.Millisecond
	p1 := &staticPoller{src: SourceGmail, envs: envs}
	p2 := &staticPoller{src: SourceGmail, envs: envs}
	w1 := newTestService(fx, p1, ing)
	w2 := newTestService(fx, p2, ing)

	var wg sync.WaitGroup
	for _, w := range []*testService{w1, w2} {
		wg.Add(1)
		go func(w *testService) { defer wg.Done(); w.tick(ctx) }(w)
	}
	wg.Wait()

	require.Equal(t, 1, p1.polls+p2.polls, "a due config must be claimed by exactly one worker")
	for i := 0; i < 5; i++ {
		require.Equal(t, 1, ing.count(fmt.Sprintf("msg-%d.md", i)), "message %d materialised once", i)
	}

	// Even when both workers do poll (manual run-now racing the ticker),
	// the message lease keeps materialisation single.
	ing2 := newCountingIngester()
	ing2.delay = 100 * time.Millisecond
	more := []*Envelope{{SourceMessageID: "gmail/x", Subject: "late", Date: time.Now()}}
	w1 = newTestService(fx, &staticPoller{src: SourceGmail, envs: more}, ing2)
	w2 = newTestService(fx, &staticPoller{src: SourceGmail, envs: more}, ing2)
	for _, w := range []*testService{w1, w2} {
		wg.Add(1)
		go func(w *testService) {
			defer wg.Done()
			_, _ = w.RunOnce(ctx, fx.tenant.String(), fx.cfgID.String())
		}(w)
	}
	wg.Wait()
	require.Equal(t, 1, ing2.count("late.md"), "concurrent runs must materialise a message once")
	require.Equal(t, "materialised", fx.statuses(ctx, t)["gmail/x"])
}
