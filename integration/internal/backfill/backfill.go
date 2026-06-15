// Package backfill onboards EXISTING ERP data into SeDoc. It enumerates
// customers + their documents from a source (the ERP listing API or an NDJSON
// file), synthesizes the canonical events, and feeds them through the SAME
// idempotent handlers the live event worker uses (sync.Syncer.Handle). Because
// those handlers are idempotent — folder creates use stable logical keys and
// :upsert dedups on (external_id, checksum) — a backfill is re-runnable: a second
// pass over the same data creates no new folders, documents, or versions.
//
// All SeDoc writes go through the shared rate limiter on the *sedoc.Client, so a
// backfill paces itself under the 600/min ceiling and shares that budget with the
// live worker running in the same process.
package backfill

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	syncpkg "github.com/aieera/sedoc/integration/internal/sync"
)

// Runner executes backfill runs.
type Runner struct {
	st          *store.Store
	syncer      *syncpkg.Syncer
	src         erp.Lister
	concurrency int
	log         zerolog.Logger
}

// NewRunner builds a Runner. concurrency bounds how many customers are processed
// in parallel; the per-write SeDoc rate is capped independently by the client's
// shared limiter.
func NewRunner(st *store.Store, syncer *syncpkg.Syncer, src erp.Lister, concurrency int, log zerolog.Logger) *Runner {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Runner{st: st, syncer: syncer, src: src, concurrency: concurrency, log: log}
}

// PollLoop claims pending runs (BFF-triggered) and processes them, one at a time,
// until ctx is cancelled. Runs in the worker process so backfill writes share the
// live worker's rate-limiter budget.
func (r *Runner) PollLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run, ok, err := r.st.ClaimPendingBackfillRun(ctx)
			if err != nil {
				r.log.Error().Err(err).Msg("claim backfill run")
				continue
			}
			if !ok {
				continue
			}
			r.log.Info().Str("run", run.ID.String()).Str("source", run.Source).Msg("backfill started")
			r.Process(ctx, run)
		}
	}
}

// customerPlan is one customer's materialized work: a customer.created item plus
// its documents (or the enumeration error that prevented listing them).
type customerPlan struct {
	ref     string
	name    string
	docs    []erp.DocumentRef
	enumErr error
}

// Process executes an already-claimed (running) run to completion: enumerate →
// set total → process per-customer under bounded concurrency → finish. Counters
// and per-item failures are persisted as it goes, so progress is queryable live.
func (r *Runner) Process(ctx context.Context, run *store.BackfillRun) {
	customers, err := r.scope(ctx, run)
	if err != nil {
		r.log.Error().Err(err).Str("run", run.ID.String()).Msg("backfill enumerate customers")
		_ = r.st.FinishBackfillRun(ctx, run.ID, "failed", "enumerate customers: "+err.Error())
		return
	}

	// Materialize the plan up front so `total` is accurate for progress. Listing
	// documents is a read (not rate-limited).
	plans := make([]customerPlan, 0, len(customers))
	total := 0
	for _, c := range customers {
		if ctx.Err() != nil {
			break
		}
		docs, derr := r.src.ListDocuments(ctx, c.Ref)
		if derr != nil {
			// The customer.created item still runs; the listing failure is its own
			// counted unit so totals stay consistent (failed ⊆ processed ⊆ total).
			plans = append(plans, customerPlan{ref: c.Ref, name: c.Name, enumErr: derr})
			total += 2
			continue
		}
		plans = append(plans, customerPlan{ref: c.Ref, name: c.Name, docs: docs})
		total += 1 + len(docs)
	}
	if err := r.st.SetBackfillTotal(ctx, run.ID, total); err != nil {
		r.log.Error().Err(err).Msg("set backfill total")
	}

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for _, p := range plans {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(p customerPlan) {
			defer wg.Done()
			defer func() { <-sem }()
			r.processCustomer(ctx, run.ID, p)
		}(p)
	}
	wg.Wait()

	status, lastErr := "completed", ""
	if ctx.Err() != nil {
		status, lastErr = "canceled", ctx.Err().Error()
	}
	_ = r.st.FinishBackfillRun(ctx, run.ID, status, lastErr)
	r.log.Info().Str("run", run.ID.String()).Str("status", status).Int("total", total).Msg("backfill finished")
}

// processCustomer provisions the customer then files each of its documents. Each
// is a separate idempotent Handle call counted toward progress.
func (r *Runner) processCustomer(ctx context.Context, runID uuid.UUID, p customerPlan) {
	r.do(ctx, runID, p.ref, "customer.created", &erp.Event{
		ERPEventID:  "backfill:cust:" + p.ref,
		Kind:        erp.KindCustomerCreated,
		CustomerRef: p.ref,
		Name:        p.name,
	})
	if p.enumErr != nil {
		_ = r.st.IncBackfillProgress(ctx, runID, 1, 1)
		_ = r.st.RecordBackfillFailure(ctx, runID, p.ref, "list-documents", p.enumErr.Error(), "")
		return
	}
	for _, d := range p.docs {
		if ctx.Err() != nil {
			return
		}
		ev, item := docEvent(p.ref, d)
		r.do(ctx, runID, p.ref, item, ev)
	}
}

// do runs one synthesized event through the shared idempotent handler, using the
// event's stable id as the SeDoc Idempotency-Key seed, and records the outcome.
func (r *Runner) do(ctx context.Context, runID uuid.UUID, ref, item string, ev *erp.Event) {
	if _, err := r.syncer.Handle(ctx, ev.ERPEventID, ev); err != nil {
		_ = r.st.IncBackfillProgress(ctx, runID, 1, 1)
		_ = r.st.RecordBackfillFailure(ctx, runID, ref, item, err.Error(), sedoc.CorrelationID(err))
		return
	}
	_ = r.st.IncBackfillProgress(ctx, runID, 1, 0)
}

// docEvent synthesizes the canonical event for an inventory document, with a
// STABLE erp_event_id so re-runs replay the same SeDoc idempotency keys.
func docEvent(ref string, d erp.DocumentRef) (*erp.Event, string) {
	if d.Kind == "attachment" {
		label := d.Filename
		if label == "" {
			label = d.FileRef
		}
		return &erp.Event{
			ERPEventID:  "backfill:att:" + ref + ":" + d.FileRef,
			Kind:        erp.KindAttachmentUploaded,
			CustomerRef: ref,
			Filename:    d.Filename,
			FileRef:     d.FileRef,
			DocType:     d.DocType,
			Mime:        d.Mime,
		}, "attachment:" + label
	}
	return &erp.Event{
		ERPEventID:  "backfill:doc:" + ref + ":" + d.DocType + ":" + d.DocNumber,
		Kind:        erp.KindDocumentCommitted,
		CustomerRef: ref,
		DocType:     d.DocType,
		DocNumber:   d.DocNumber,
		Status:      d.Status,
		Filename:    d.Filename,
		FileRef:     d.FileRef,
		Mime:        d.Mime,
	}, d.DocType + "-" + d.DocNumber
}

// scope decides which customers a run covers. For the 'erp' source a non-empty
// source_arg is a comma-separated allow-list of customer_refs (targeted re-run);
// empty means the full inventory. Other sources (ndjson) enumerate everything the
// in-process source holds.
func (r *Runner) scope(ctx context.Context, run *store.BackfillRun) ([]erp.Customer, error) {
	if run.Source == "erp" && strings.TrimSpace(run.SourceArg) != "" {
		var out []erp.Customer
		for _, ref := range strings.Split(run.SourceArg, ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				out = append(out, erp.Customer{Ref: ref})
			}
		}
		return out, nil
	}
	return r.src.ListCustomers(ctx)
}
