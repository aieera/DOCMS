package sync

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
)

// Worker drains the DB-backed job queue: claim due pending jobs, apply them to
// SeDoc via the Syncer, and on failure either back off (retryable) or
// dead-letter (terminal / attempts exhausted).
type Worker struct {
	st          *store.Store
	syncer      *Syncer
	log         zerolog.Logger
	concurrency int
	maxAttempts int
	poll        time.Duration
	lease       time.Duration
	metrics     *Metrics // optional; nil-safe
}

// WorkerOptions configures the loop.
type WorkerOptions struct {
	Concurrency int
	MaxAttempts int
	Poll        time.Duration
	Lease       time.Duration
	Metrics     *Metrics // dashboard counters; nil disables metric recording
}

// NewWorker constructs a Worker with sensible defaults.
func NewWorker(st *store.Store, syncer *Syncer, log zerolog.Logger, o WorkerOptions) *Worker {
	if o.Concurrency <= 0 {
		o.Concurrency = 8
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 6
	}
	if o.Poll <= 0 {
		o.Poll = 2 * time.Second
	}
	if o.Lease <= 0 {
		o.Lease = 5 * time.Minute
	}
	return &Worker{st: st, syncer: syncer, log: log, concurrency: o.Concurrency,
		maxAttempts: o.MaxAttempts, poll: o.Poll, lease: o.Lease, metrics: o.Metrics}
}

// Run loops until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.drain(ctx)
		}
	}
}

// drain claims one batch and processes it concurrently. Returns after the batch
// so the loop re-polls; an empty claim is a cheap no-op.
func (w *Worker) drain(ctx context.Context) {
	jobs, err := w.st.ClaimDue(ctx, w.concurrency, w.lease)
	if err != nil {
		w.log.Error().Err(err).Msg("claim due jobs")
		return
	}
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j store.SyncJob) {
			defer wg.Done()
			w.process(ctx, j)
		}(jobs[i])
	}
	wg.Wait()
}

func (w *Worker) process(ctx context.Context, j store.SyncJob) {
	e, derr := DecodeEvent(j.Payload)
	if derr != nil {
		// Unparseable payload never succeeds — dead-letter immediately.
		_ = w.st.MarkFailed(ctx, j.ID, "decode payload: "+derr.Error(), "")
		return
	}
	res, err := w.syncer.Handle(ctx, j.ID.String(), e)
	if err == nil {
		_ = w.st.MarkDone(ctx, j.ID, res, "")
		w.metrics.OnProcessed()
		return
	}
	if sedoc.HTTPStatus(err) == 429 { // throttle breach — should be rare with the proactive limiter
		w.metrics.On429()
	}

	corr := sedoc.CorrelationID(err)
	var verr *erp.ValidationError
	terminal := errors.As(err, &verr) || !sedoc.IsRetryable(err)
	if terminal || j.Attempts >= w.maxAttempts {
		w.log.Warn().Err(err).Str("job", j.ID.String()).Str("kind", j.Kind).
			Int("attempts", j.Attempts).Str("correlation_id", corr).Msg("dead-lettering job")
		_ = w.st.MarkFailed(ctx, j.ID, err.Error(), corr)
		return
	}
	backoff := sedoc.RetryAfter(err)
	if backoff <= 0 {
		backoff = w.backoff(j.Attempts)
	}
	w.log.Info().Err(err).Str("job", j.ID.String()).Int("attempts", j.Attempts).
		Dur("backoff", backoff).Str("correlation_id", corr).Msg("retrying job")
	_ = w.st.MarkRetry(ctx, j.ID, backoff, err.Error(), corr)
}

// backoff is exponential (2s, 4s, 8s, …) capped at 5m. attempts is the post-claim
// count, so the first failure (attempts=1) waits ~2s.
func (w *Worker) backoff(attempts int) time.Duration {
	d := time.Duration(1<<uint(attempts)) * time.Second
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
