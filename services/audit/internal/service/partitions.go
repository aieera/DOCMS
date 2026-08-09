package service

import (
	"context"
	"fmt"
	"time"
)

// Partition maintenance scheduling (BUG-01).
//
// The missing half of the audit_events partitioning story: migration
// 000005 supplies the SQL, this supplies the schedule. It runs in-process
// rather than as an operator cron job because the previous design — a
// comment in a migration saying "production operators roll new partitions
// forward via a cron job" — is exactly how the audit log ended up empty.
// A guarantee that depends on someone reading a SQL comment is not a
// guarantee.

const (
	// PartitionMonthsAhead is how far forward each maintenance pass keeps
	// partitions provisioned. Twelve months means the log survives a full
	// year with the maintainer completely dead, on top of the DEFAULT
	// partition that already makes a routing failure impossible.
	PartitionMonthsAhead = 12

	// PartitionInterval is the steady-state cadence. Monthly would be
	// enough; daily is simpler, costs one no-op query a day, and shrinks
	// the window in which a restored-from-backup or clock-skewed database
	// is running without next month's partition.
	PartitionInterval = 24 * time.Hour

	// PartitionRetryInterval is the cadence used after a failed pass, so a
	// transient database problem at boot is retried in minutes rather than
	// leaving the log riding on the DEFAULT partition for a day.
	PartitionRetryInterval = 5 * time.Minute
)

// EnsurePartitions runs one maintenance pass: provision the rolling
// partition window and re-read how many rows are stranded in DEFAULT.
func (s *Service) EnsurePartitions(ctx context.Context) error {
	created, err := s.repo.EnsurePartitions(ctx, PartitionMonthsAhead)
	if err != nil {
		auditPartitionEnsureTotal.WithLabelValues("error").Inc()
		return fmt.Errorf("audit partition maintenance: %w", err)
	}
	auditPartitionEnsureTotal.WithLabelValues("ok").Inc()
	if created > 0 {
		auditPartitionCreatedTotal.Add(float64(created))
		s.log.Info().
			Int("created", created).
			Int("months_ahead", PartitionMonthsAhead).
			Msg("audit: provisioned audit_events partitions")
	}

	// Occupancy of the safety net is reported separately: a probe failure
	// is not a maintenance failure, so it must not flip the pass to error
	// and trigger the fast retry.
	n, derr := s.repo.DefaultPartitionRows(ctx)
	if derr != nil {
		s.log.Warn().Err(derr).Msg("audit: DEFAULT partition occupancy probe failed")
		return nil
	}
	auditDefaultPartitionRows.Set(float64(n))
	if n > 0 {
		s.log.Warn().
			Int64("rows", n).
			Msg("audit: rows are sitting in the audit_events DEFAULT partition — captured and queryable, but their created_at fell outside the maintained window")
	}
	return nil
}

// StartPartitionMaintainer runs the first pass SYNCHRONOUSLY — a freshly
// booted audit service must not start consuming NATS against a table with
// no partition for today — and then keeps a background goroutine running
// that repeats it until parent is cancelled.
//
// The first pass's error is returned so the caller can be loud about it;
// the background loop starts either way and retries at
// PartitionRetryInterval until a pass succeeds, then settles back to
// PartitionInterval.
func (s *Service) StartPartitionMaintainer(parent context.Context) error {
	return startMaintainer(parent, PartitionInterval, PartitionRetryInterval, s.EnsurePartitions)
}

// startMaintainer is the scheduling loop, split out from
// StartPartitionMaintainer so the cadence contract (synchronous first
// pass, fast retry after failure, back to the slow cadence after success,
// stop on context cancellation) is unit-testable without a database.
//
// done, when non-nil, receives the outcome of every completed pass
// (including the first) — a test-only seam; production passes nothing.
// All sends happen on the background goroutine, so a slow reader delays
// the next tick instead of blocking the caller.
func startMaintainer(
	parent context.Context,
	interval, retryInterval time.Duration,
	pass func(context.Context) error,
	done ...chan<- error,
) error {
	if parent == nil {
		parent = context.Background()
	}
	var notify chan<- error
	if len(done) > 0 {
		notify = done[0]
	}

	next := func(err error) time.Duration {
		if err != nil {
			return retryInterval
		}
		return interval
	}

	firstErr := pass(parent)

	go func() {
		report := func(err error) bool {
			if notify == nil {
				return true
			}
			select {
			case notify <- err:
				return true
			case <-parent.Done():
				return false
			}
		}
		if !report(firstErr) {
			return
		}
		t := time.NewTimer(next(firstErr))
		defer t.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-t.C:
				err := pass(parent)
				if !report(err) {
					return
				}
				t.Reset(next(err))
			}
		}
	}()

	return firstErr
}
