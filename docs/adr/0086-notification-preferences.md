# ADR 0086 — Unified Notification Preferences

Date: 2026-05-09
Status: Accepted
Supersedes: nothing (was scheduled as ADR 0069 in §10.7; 0069 is
taken by Federated Search Admin. Same shift pattern as the
displaced 0061-0068 ADRs that have been renumbered to 0078-0085 —
this ADR uses the next free slot above the existing range to avoid
disturbing 0069.)

## Context

The notification service ([services/notification/](../../services/notification/))
already ships:

- An NATS consumer subscribed to `dms.notify.>` ([service.go](../../services/notification/internal/service/service.go))
  that writes inbox rows + sends emails per a `DeliveryPayload`.
- A flat per-user preference (one bool per channel: email/push/
  slack/sms) stored as a single denormalized row, despite the
  schema's `notification_preferences` table having a composite PK
  on `(tenant, user, channel, event_type)`.
- An in-app inbox (`notifications` table) with read/unread state.

§10.7 widens this to:

1. **Per-user × per-event-type × per-channel** matrix. The user
   should be able to say "comments-on-my-docs" → in-app + email,
   not slack. Schema already supports this; the service layer
   must.
2. **Snooze** — "mute comments for 1h" — independent of the
   permanent matrix.
3. **Global DND hours** — "no pings between 18:00 and 08:00 in
   my timezone".
4. **Digest** — when `digest_enabled` is on for a (user,
   event_type, channel) cell, batch events of that type within a
   5-minute window into a single digest notification.
5. **Channels reused from §6.3**: in-app, email, push, Slack,
   Teams, SMS. The schema CHECK already lists them; the consumer
   only emails today.

## Decision

### Schema deltas (migration 000034)

```sql
-- 1. digest flag per matrix cell.
ALTER TABLE notification_preferences
  ADD COLUMN IF NOT EXISTS digest_enabled BOOLEAN NOT NULL DEFAULT FALSE;

-- 2. snoozes: a user can mute one event_type until a specific
--    timestamp. Multiple snoozes per user (one per event_type)
--    allowed; the most-recent matching row wins.
CREATE TABLE notification_snoozes (
  tenant_id    UUID NOT NULL,
  id           UUID NOT NULL DEFAULT gen_random_uuid(),
  user_id      UUID NOT NULL,
  event_type   TEXT NOT NULL,        -- e.g. 'comment.mention', '*' for all
  until_at     TIMESTAMPTZ NOT NULL,
  reason       TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);

-- 3. global DND per user. One row per user; nullable means "no DND".
--    Times stored as "HH:MM" with a tz field so cross-tz tenants
--    work.
CREATE TABLE notification_dnd (
  tenant_id    UUID PRIMARY KEY,     -- composite via user_id below
  user_id      UUID NOT NULL,
  dnd_start    TIME NOT NULL,
  dnd_end      TIME NOT NULL,
  timezone     TEXT NOT NULL DEFAULT 'UTC',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 4. digest accumulator. Each in-flight digest is one row; the
--    payload column holds an array of the events that have folded
--    in. flush_after_at = first-event-arrival + 5 min.
CREATE TABLE notification_digests (
  tenant_id        UUID NOT NULL,
  id               UUID NOT NULL DEFAULT gen_random_uuid(),
  user_id          UUID NOT NULL,
  event_type       TEXT NOT NULL,
  channel          TEXT NOT NULL,
  events           JSONB NOT NULL DEFAULT '[]'::jsonb,
  count            INTEGER NOT NULL DEFAULT 0,
  flush_after_at   TIMESTAMPTZ NOT NULL,
  flushed_at       TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id),
  -- One open (un-flushed) row per (user, event_type, channel).
  UNIQUE (tenant_id, user_id, event_type, channel) WHERE flushed_at IS NULL
);
```

All four tables are RLS-isolated on `tenant_id`.

### Decide() pipeline

`Decide(payload) -> []Channel` is the new pre-delivery gate. It
runs ONCE per recipient when a `dms.notify.*` event lands, before
the existing `Deliver` path:

```
for each user_id in payload.user_ids:
  channels = []
  prefs = LoadPrefs(tenant, user, event_type)        // matrix
  if prefs is empty: prefs = DefaultsForEventType()   // 'in_app' on; rest off

  if SnoozeActive(tenant, user, event_type, now):    // explicit mute
    skip user entirely

  if InDNDWindow(tenant, user, now):                  // global quiet hours
    keep only 'in_app'  // visible on next visit; never email/sms/push during DND

  for cell in prefs:
    if cell.is_enabled and cell.channel not blocked:
      if cell.digest_enabled:
        UpsertDigest(...) — accumulate, return [] for this channel
      else:
        channels = channels + [cell.channel]
  Deliver(user, channels)  // existing path
```

The legacy flat-pref code path stays for back-compat with rows
that haven't been re-saved in the new shape. A migration helper
upserts default per-event-type rows on first read for any user
whose flat prefs exist.

### Digest flush ticker

A 1-minute ticker scans `notification_digests` for rows where
`flushed_at IS NULL AND flush_after_at <= now()`. Each match:
1. `UPDATE … SET flushed_at = now() … RETURNING events, count` —
   atomic claim, idempotent like the task sweep in ADR 0068.
2. Build a single digest payload: title `"3 new comment mentions"`,
   body listing the first 3 + "and N more", same `resource_type`/
   `resource_id` shape the consumer's `Deliver` already handles.
3. Call the existing channel-specific senders.

### API surface

```
GET    /api/v1/notifications/preferences/matrix          — full grid
PUT    /api/v1/notifications/preferences/matrix          — bulk replace
PATCH  /api/v1/notifications/preferences/cell            — single cell
                                                            { event_type, channel, is_enabled, digest_enabled }

GET    /api/v1/notifications/snoozes                     — list mine
POST   /api/v1/notifications/snooze                      — { event_type, duration_minutes }
DELETE /api/v1/notifications/snooze/{id}

GET    /api/v1/notifications/dnd                         — read mine
PUT    /api/v1/notifications/dnd                         — { start, end, timezone }
DELETE /api/v1/notifications/dnd                         — clear
```

The legacy flat `GET/PUT /preferences` stay for back-compat.

### Frontend

A single `/settings/notifications` page with three blocks:

1. **Matrix grid** — rows = event types, cols = channels, cells
   are checkboxes for enable + a small "digest" badge toggle.
   Defaults shown ghost-italicized; the first edit upserts a
   concrete row.
2. **Do not disturb** — start/end picker + tz autodetect.
3. **Active snoozes** — list of current snoozes with a
   "Cancel" button.

Per-notification snooze: every notification card on the inbox + the
in-app dropdown gets a "snooze this type for 1h" link that POSTs
the snooze API.

## Consequences

- The schema already had the right shape. Most of the work is
  service-layer Decide() + new tables for snooze/DND/digests.
- Digests change the perceived latency of bursty events:
  previously every comment mention emailed instantly; with digest
  on, the user gets one email up to 5 minutes later. Off by
  default. Surfaced in the UI as "I want fewer pings".
- Decide() runs per recipient per event. For a comment with 5
  mentions that's 5 prefs lookups + 5 snooze checks + 5 DND
  checks. We Redis-cache (60s) the prefs + DND read so the hot
  path is one Postgres round-trip per fan-out, not five.
- Snoozes never expire automatically — `until_at` is the cutoff;
  a daily housekeeping query removes rows where `until_at <
  now() - 7 days` so the table doesn't grow unbounded.
- Slack / Teams / Push / SMS senders aren't wired in this ADR
  (today's consumer only sends email). When channel adapters land
  later, they slot into the existing channel-keyed send map; no
  Decide() change.

## Out of scope

- Per-tenant DND override (e.g. "no notifications during company
  holidays"). Per-user DND covers the user-facing 80%.
- Channel-specific digest formatting (HTML email templates per
  event type). The first-cut digest is plain text.
- Webhook channel (custom HTTP endpoint per user). Add when a
  customer asks; the channel-keyed map makes this a small
  addition.
