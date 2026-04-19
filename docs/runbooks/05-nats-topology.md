# Runbook — NATS JetStream topology (Wave 5 Prompt 5.2)

**Last rehearsed:** 2026-04-17 (dev box, live bootstrap + replay verified).
**On-call:** platform team.

## Streams at a glance

| Stream | Subjects | Retention | DLQ |
|---|---|---|---|
| DOC_EVENTS | `dms.document.>`, `dms.version.>`, `dms.workspace.>` | 168h | DOC_EVENTS_DLQ (720h) |
| USER_EVENTS | `dms.user.>`, `dms.session.>`, `dms.apikey.>` | 168h | USER_EVENTS_DLQ |
| POLICY_EVENTS | `dms.policy.>`, `dms.permission.>` | 168h | POLICY_EVENTS_DLQ |
| BILLING_EVENTS | `dms.billing.>`, `dms.subscription.>`, `dms.usage.>` | 168h | BILLING_EVENTS_DLQ |
| AUDIT_EVENTS | `dms.audit.>` | 168h | AUDIT_EVENTS_DLQ |
| SEARCH_EVENTS | `dms.search.>` | 168h | SEARCH_EVENTS_DLQ |
| WORKFLOW_EVENTS | `dms.workflow.>`, `dms.task.>` | 168h | WORKFLOW_EVENTS_DLQ |
| INTEL_EVENTS | `dms.ocr.>`, `dms.classify.>`, `dms.embed.>`, `dms.ner.>` | 168h | INTEL_EVENTS_DLQ |
| NOTIFY_EVENTS | `dms.notify.>` | 168h | NOTIFY_EVENTS_DLQ |
| LEGACY_EVENTS | `dms.sharelink.>`, `dms.folder.>`, `dms.intelligence.>`, `dms.rotation.>` | 168h | LEGACY_EVENTS_DLQ |

Config per stream: `Retention=Limits`, `Discard=Old`, `Duplicates=2m`,
`Storage=File`, `Replicas=${VAULTDMS_NATS_REPLICAS:-1}` (set to 3 in prod).

DLQ subjects follow `dms.dlq.<stream_name_lower>.>`.

## Bootstrap

Any service calling `events.ConnectNATS` runs `EnsureStreams` implicitly
and idempotently. For standalone runs (pre-deploy, DR restore):

```bash
./dms-admin nats bootstrap --dry-run      # print diff, no changes
./dms-admin nats bootstrap                # apply, idempotent
./dms-admin nats bootstrap --purge-legacy # one-shot; see below
./dms-admin nats list                     # verify
./dms-admin nats replay --stream DOC_EVENTS --count 20
```

`--purge-legacy` deletes the pre-Wave-5 stream names (DOCUMENTS, AUTH,
WORKFLOWS, AUDIT, NOTIFICATIONS, BILLING, INTELLIGENCE) before creating
the new topology. Needed **exactly once** per environment during the
Wave 5 upgrade. Subsequent runs with the flag are no-ops because those
names no longer exist.

## The silent-drop fix

Before Wave 5, the topology bound `dms.document.>` and `dms.version.>`
but **not** `dms.user.*`, `dms.policy.*`, `dms.session.*`, or
`dms.apikey.*`. Services happily published to those subjects; NATS
accepted the messages; the broker routed to no stream and the messages
vanished. Symptoms included outbox drain stalls ("nats: no response
from stream") and zero SCIM/auth event flow downstream.

After Prompt 5.2, every publish in the codebase lands in a stream.
The [unit test](../../pkg/events/publisher_test.go)
`TestDefaultStreamsCoverEveryKnownSubject` is the canary — if a new
event introduces a prefix no stream covers, CI fails.

## What can go wrong

### `add stream X: subjects overlap with an existing stream`

A legacy stream still binds subjects the new stream wants. Run with
`--purge-legacy`. If the overlap is with a non-legacy stream, there's
a topology bug — two `StreamSpec`s claim the same subject. Check
`TestNoSubjectOverlap` in `pkg/events/publisher_test.go`; fix and ship.

### `consumer is starved`

Stream has messages but a consumer has `num_pending > 0` and
`delivered.consumer_seq` stuck. Possibilities:
- Consumer code is crashing silently. Check service logs.
- Consumer's durable name collides with another pod. Run
  `nats consumer info <stream> <durable>` and check `num_waiting`.

### `DLQ filling up`

`DOC_EVENTS_DLQ` (or any other) accumulating means consumers are
retrying-to-death. Dedupe key (`event_id`) should be unchanged across
retries — if the event id is rotating per attempt, the consumer's code
is regenerating it. Fix the consumer, then replay the DLQ:

```bash
./dms-admin nats replay --stream DOC_EVENTS_DLQ --count 100
```

## Rollback

Reverting Prompt 5.2 means going back to a broken topology. Don't.

If you must revert for a specific incident:

```bash
# Delete the new streams (DATA LOSS for unconsumed messages).
for S in DOC_EVENTS USER_EVENTS POLICY_EVENTS BILLING_EVENTS AUDIT_EVENTS \
         SEARCH_EVENTS WORKFLOW_EVENTS INTEL_EVENTS NOTIFY_EVENTS LEGACY_EVENTS; do
  nats stream delete "$S" -f
  nats stream delete "${S}_DLQ" -f
done
# Restore legacy via git revert of pkg/events/publisher.go and re-run
# bootstrap from a pre-Wave-5 binary.
```

Consumers subscribing to the old stream names will need their
`BindStream` call updated to the new names via code revert.

## Metrics to watch (post-landing)

- `nats_jetstream_stream_messages{stream=...}` — per-stream message count.
- `nats_jetstream_consumer_num_pending{consumer=...}` — flag > 1000 for
  more than 5 minutes.
- `nats_jetstream_stream_bytes{stream=~".*_DLQ"}` — DLQ growing indicates
  poisoned-message feedback loop.
