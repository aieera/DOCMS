# ADR 0085 — Saved-search alerts + subscribers

Date: 2026-05-06
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0059 in §7.6; 0059 is taken
by Classification Corrections. Same shift as 0078-0067.)

## Context

`saved_searches` already exists from `000001_saved_searches.up.sql`
with `name`, `query`, `filters`, `notify`, `notify_interval_minutes`,
`last_run_at`. POST/GET/DELETE endpoints are wired. What's missing
for the §7.6 spec:

1. **Subscribers** — an alert author should be able to fan out
   notifications to a team without each member having to clone the
   search. The current shape is one-saved-search-per-user.
2. **Edit / convert-to-alert** — saving a search and later flipping
   it to an alert is a user-flow we don't expose. PATCH endpoint
   missing.
3. **Diff cursor** — to avoid notifying on every existing doc each
   run, we need to remember the previous run's doc-id set. Currently
   no such field.
4. **Cron-style schedule** — `notify_interval_minutes` covers
   "every N minutes" but not "9am every weekday" or other calendar-
   shaped schedules. Spec calls for `alert_frequency`.
5. **Per-channel preferences** — email, in-app, digest. Currently a
   single `notify` boolean, no per-subscriber channel choice.
6. **Temporal scheduled workflow** — running on a cron. The workflow
   service has the SDK + activities pattern; we add a new
   `SavedSearchAlertWorkflow` that owns the loop.

## Decision

### Schema

`saved_searches` (existing) gains three columns:

| Column | Purpose |
|--------|---------|
| `last_match_doc_ids` (JSONB) | Snapshot of the previous run's doc_id set. Workflow diffs current vs. this; emits one notification per NEW id. |
| `alert_frequency_cron` (TEXT) | Optional cron expression. When set, takes precedence over `notify_interval_minutes`. Empty = legacy interval-minutes mode. |
| `workflow_id` (TEXT) | The Temporal workflow handle the alert is bound to. Lookup key for cancel-on-delete + pause. |

`saved_search_subscribers` (new):

| Column | Purpose |
|--------|---------|
| `tenant_id`, `saved_search_id`, `user_id` | composite PK |
| `channels` (TEXT[]) | One or more of `in_app`, `email`, `digest`. Multiple allowed — a subscriber can want both in-app AND email for the same alert. |
| `subscribed_by` (UUID) | Audit: who added this subscription. The owner self-subscribes by default; admins can bulk-subscribe a team. |
| `subscribed_at` (TIMESTAMPTZ) | Audit timestamp. |

Both tables RLS-isolated.

### API

| Method | Path | Notes |
|--------|------|-------|
| POST | `/api/v1/saved-searches` | Existing — also auto-subscribes owner with default channels when `notify=true`. |
| GET | `/api/v1/saved-searches` | Existing — response now embeds `subscribers[]` and `subscriber_count`. |
| PATCH | `/api/v1/saved-searches/{id}` | **NEW** — edit name/query/filters; flip notify on/off (convert to alert); update interval/cron. |
| DELETE | `/api/v1/saved-searches/{id}` | Existing — also cancels the bound workflow + cascade-deletes subscribers. |
| POST | `/api/v1/saved-searches/{id}/subscribe` | **NEW** — body `{user_id, channels}`. Owner OR admin. |
| DELETE | `/api/v1/saved-searches/{id}/subscribe/{user_id}` | **NEW** — unsubscribe. A user can always remove themselves; admin can remove anyone. |

### Temporal workflow

`SavedSearchAlertWorkflow(saved_search_id)` — long-running parent
workflow per alert:

```
loop until cancelled:
    sleep next_interval()    // cron-aware via robfig/cron parser
    run_search(saved_search_id) → current_doc_ids
    new_ids = current_doc_ids - last_match_doc_ids
    for each subscriber, for each channel:
        emit dms.notify.saved_search_match.v1
    update last_match_doc_ids = current_doc_ids
    update last_run_at = now
```

Cron-child loop pattern (not Temporal Schedules) because:
- The workflow service runs on Temporal SDK 1.22-ish; Schedules
  need 1.23+.
- The cron-child shape is the documented pattern in the existing
  `RetentionWorkflow` + `EraseWorkflow`.
- Cancellation via signal is uniform across the existing workflows.

`workflow_id` on the saved_searches row is the handle. Delete
saves cancel by id; PATCH that flips notify off cancels; PATCH that
flips it on starts a new workflow.

### Notifications

New event: `dms.notify.saved_search_match.v1`. Subject:
`saved_search/{id}`. Data:

```json
{
  "tenant_id":         "...",
  "saved_search_id":   "...",
  "saved_search_name": "open contracts",
  "subscriber_id":     "...",
  "channel":           "in_app|email|digest",
  "matched_doc_ids":   ["doc-1", "doc-2"]
}
```

Bound to `NOTIFY_EVENTS` JetStream stream (already has
`dms.notify.>` binding). The notifications service consumes,
delivers via the per-channel adapters, and writes the in-app
notification row.

## Consequences

- **Per-subscriber channels.** Email + in-app + digest can fan out
  off the same alert without duplicating the saved search.
- **No false-positive flood on alert creation.** First run seeds
  `last_match_doc_ids` from the current result set; only subsequent
  runs emit notifications. The alert "starts watching" rather than
  "notifies you about every existing match".
- **Workflow cancellation must be idempotent.** A delete that races
  with a workflow-tick must not strand the workflow. The handler
  cancels via signal and tolerates `WorkflowNotFound`.
- **Cron is optional.** Tenants on the simple "every N minutes"
  shape don't pay the cron-parse complexity. The workflow's
  next-interval logic prefers `alert_frequency_cron` when set, else
  `notify_interval_minutes`.

## Amendment (2026-07-03) — implementation notes + hardening

The shipped implementation diverged from two details above, and a
hardening pass closed three gaps found in review:

1. **Scheduling is Temporal Schedules, not a cron-child loop.** One
   Schedule per alert (`saved-search-alert-<id>`), synced to the
   `notify` flag by a 60s reconcile loop in the workflow worker
   (`saved_search_alert_schedule.go`). The `workflow_id` column the
   original design called for is vestigial and unused.
2. **Emission goes through the transactional outbox (C5).** The
   original code direct-published `dms.notify.saved_search_match.v1`
   via `JS.Publish` from the activity, bypassing the outbox (and
   evading the case-sensitive C5 archtest, which is now
   case-insensitive). `EmitSavedSearchMatch` now inserts an outbox row
   in a tenant tx; the shared outbox publisher ships it.
3. **Subscriber channels actually reach delivery.** The event now
   carries the subscriber's chosen channels as
   `DeliveryPayload.channels` — per-event channel consent the
   notification service's `Decide` honors (delivering on a consented
   channel even without a matrix cell, but never overriding snooze,
   DND, an explicit matrix disable, or the flat per-channel switch;
   `"digest"` folds email through the ADR 0086 digest table). One
   event per subscriber (previously one per (subscriber, channel),
   which also duplicated in-app rows). The digest flusher passes the
   same hint so digest summaries deliver on their folded channel.
4. **Cursor-first ordering.** `last_match_doc_ids` is written BEFORE
   emission and its failure is fatal (Temporal retries the idempotent
   update). Previously emission ran first and a cursor-write failure
   re-notified every subscriber next tick — the comment claiming a
   notification-side `(saved_search_id, doc_id, day)` dedup was
   wrong; no such dedup exists. Delivery is now at-most-once per
   window: a crash between cursor write and emission skips that
   batch's notifications (the documents remain in the app).
5. **Alert-at-save.** The web save-search flow offers "Alert me when
   new documents match" (+ interval) at creation instead of requiring
   a second step on the manage page.
