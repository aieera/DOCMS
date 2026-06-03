// ADR 0085 — Saved-search alert workflow.
//
// One Temporal schedule per saved-search alert. The schedule fires
// the SavedSearchAlertWorkflow on the cadence the user configured
// (cron when set, else `every <interval_minutes>` derived spec).
// The workflow:
//   1. fetches the saved search row + its subscribers
//   2. runs the search
//   3. diffs current doc-id set vs. last_match_doc_ids
//   4. emits dms.notify.saved_search_match.v1 per (subscriber, channel)
//      for each NEW doc_id
//   5. updates last_match_doc_ids + last_run_at
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

	// 4. Fan out notifications. One activity call per (subscriber,
	//    channel) so a slow email backend doesn't block the in-app
	//    delivery for the same subscriber.
	if len(out.NewMatches) > 0 && len(loaded.Subscribers) > 0 {
		for _, sub := range loaded.Subscribers {
			for _, channel := range sub.Channels {
				if err := workflow.ExecuteActivity(ctx,
					"EmitSavedSearchMatch",
					activities.EmitMatchInput{
						TenantID:        loaded.TenantID,
						SavedSearchID:   in.SavedSearchID,
						SavedSearchName: loaded.Name,
						SubscriberID:    sub.UserID,
						Channel:         channel,
						MatchedDocIDs:   out.NewMatches,
					}).Get(ctx, nil); err != nil {
					// Per-subscriber failure isn't fatal — the others
					// still need to land. Log + continue.
					logger.Warn("subscriber notification failed",
						"subscriber_id", sub.UserID, "channel", channel, "err", err)
					continue
				}
				out.NotificationsOut++
			}
		}
	}

	// 5. Update the diff cursor + last_run_at. Always — even on a
	//    no-new-matches tick — so the cursor stays current and a
	//    later doc that matches the previous set doesn't get a
	//    spurious notification.
	if err := workflow.ExecuteActivity(ctx,
		"UpdateSavedSearchAlertCursor",
		activities.UpdateCursorInput{
			SavedSearchID:   in.SavedSearchID,
			LastMatchDocIDs: matched,
		}).Get(ctx, nil); err != nil {
		// Non-fatal. The next run will use a stale cursor and may
		// re-emit, but the notifications service dedupes by
		// (saved_search_id, doc_id, day) so the user won't see a
		// flood.
		logger.Warn("cursor update failed", "err", err)
	}

	return out, nil
}
