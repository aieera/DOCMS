// ADR 0085 — Saved-search alert workflow.
//
// One Temporal schedule per saved-search alert. The schedule fires
// the SavedSearchAlertWorkflow on the cadence the user configured
// (cron when set, else `every <interval_minutes>` derived spec).
// The workflow:
//  1. fetches the saved search row + its subscribers
//  2. runs the search
//  3. diffs current doc-id set vs. last_match_doc_ids
//  4. emits dms.notify.saved_search_match.v1 per (subscriber, channel)
//     for each NEW doc_id
//  5. updates last_match_doc_ids + last_run_at
//
// Determinism: no time.Now in the workflow body; everything routed
// through activities.
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/aieera/sedoc/services/workflow/internal/activities"
)

// SavedSearchAlertInput identifies which saved search to evaluate.
type SavedSearchAlertInput struct {
	SavedSearchID string `json:"saved_search_id"`
	TenantID      string `json:"tenant_id"`
}

// SavedSearchAlertOutcome summarises one tick.
type SavedSearchAlertOutcome struct {
	SavedSearchID    string   `json:"saved_search_id"`
	NewMatches       []string `json:"new_matches"`
	NotificationsOut int      `json:"notifications_out"`
	Skipped          bool     `json:"skipped"` // true when alert is paused or absent
}

// SavedSearchAlertWorkflow is the per-tick body. The Temporal
// schedule re-invokes it on the configured cadence; this workflow
// does NOT loop internally.
func SavedSearchAlertWorkflow(ctx workflow.Context, in SavedSearchAlertInput) (*SavedSearchAlertOutcome, error) {
	logger := workflow.GetLogger(ctx)
	out := &SavedSearchAlertOutcome{SavedSearchID: in.SavedSearchID}

	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 1 * time.Second,
			MaximumInterval: 30 * time.Second,
			MaximumAttempts: 3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	// 1. Fetch the saved search + its subscribers in one activity
	//    so the workflow doesn't pay two round-trips.
	// Activity reference is by name (the method receiver isn't
	// importable directly in workflow code — Temporal resolves the
	// activity at dispatch time from the worker's registry).
	var loaded activities.SavedSearchAlertLoaded
	if err := workflow.ExecuteActivity(ctx,
		"LoadSavedSearchAlert", in.SavedSearchID).Get(ctx, &loaded); err != nil {
		return nil, err
	}
	if !loaded.NotifyEnabled {
		// Alert was disabled between schedule fire and now; treat as a
		// no-op rather than emitting a spurious event.
		out.Skipped = true
		logger.Info("saved-search alert paused", "saved_search_id", in.SavedSearchID)
		return out, nil
	}

	// 2. Run the search via the existing search service. Returns
	//    just doc_ids — names + scores aren't needed for the diff.
	var matched []string
	if err := workflow.ExecuteActivity(ctx,
		"RunSavedSearchAlert", activities.RunSavedSearchInput{
			SavedSearchID: in.SavedSearchID,
			TenantID:      loaded.TenantID,
			OwnerUserID:   loaded.OwnerUserID,
			OwnerGroupIDs: loaded.OwnerGroupIDs,
			Query:         loaded.Query,
			Filters:       loaded.Filters,
		}).Get(ctx, &matched); err != nil {
		return nil, err
	}

	// 3. Diff. The alert workflow emits notifications ONLY for
	//    doc_ids that weren't in the previous run. This is the
	//    "first run seeds, second run notifies" semantic from the
	//    ADR — keeps a fresh alert from flooding the team with
	//    every existing match.
	prev := map[string]bool{}
	for _, id := range loaded.LastMatchDocIDs {
		prev[id] = true
	}
	for _, id := range matched {
		if !prev[id] {
			out.NewMatches = append(out.NewMatches, id)
		}
	}

	// 4. Advance the diff cursor BEFORE emitting. Always — even on a
	//    no-new-matches tick — so the cursor stays current. Ordering
	//    is deliberate: a failure after emission would re-emit the
	//    same doc_ids on the next tick (there is NO notification-side
	//    dedup — the cursor is the only guard), spamming every
	//    subscriber. Cursor-first makes delivery at-most-once per
	//    window instead: the rare crash between cursor write and
	//    emission skips one notification batch, and the documents
	//    remain visible in the app either way. Failure here is FATAL
	//    so Temporal's activity retries (idempotent UPDATE) run
	//    before anything is emitted.
	if err := workflow.ExecuteActivity(ctx,
		"UpdateSavedSearchAlertCursor",
		activities.UpdateCursorInput{
			SavedSearchID:   in.SavedSearchID,
			LastMatchDocIDs: matched,
		}).Get(ctx, nil); err != nil {
		return nil, err
	}

	// 5. Fan out notifications: one event per SUBSCRIBER, carrying
	//    that subscriber's chosen channels as the DeliveryPayload
	//    consent hint (single in-app row; email/digest per choice).
	if len(out.NewMatches) > 0 && len(loaded.Subscribers) > 0 {
		for _, sub := range loaded.Subscribers {
			if err := workflow.ExecuteActivity(ctx,
				"EmitSavedSearchMatch",
				activities.EmitMatchInput{
					TenantID:        loaded.TenantID,
					SavedSearchID:   in.SavedSearchID,
					SavedSearchName: loaded.Name,
					SubscriberID:    sub.UserID,
					Channels:        sub.Channels,
					MatchedDocIDs:   out.NewMatches,
				}).Get(ctx, nil); err != nil {
				// Per-subscriber failure isn't fatal — the others
				// still need to land. Log + continue.
				logger.Warn("subscriber notification failed",
					"subscriber_id", sub.UserID, "err", err)
				continue
			}
			out.NotificationsOut++
		}
	}

	return out, nil
}
