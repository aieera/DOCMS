# Chaos 03 — Clock skew

## Premise

Advance the system clock on one pod by +5 minutes for 5 minutes.
Temporal workflows and time-based logic must not misbehave.

## Setup

- Chaos-mesh CRD: `TimeChaos` with `timeOffset: +5m`, target
  the workflow worker pod.
- Active work: a pending retention cron schedule + a 72-h
  review workflow created 2 min before the test.

## Verification

- Retention cron doesn't fire twice in the 5-min window (one
  skewed pod shouldn't trigger a duplicate tick).
- Review-workflow 72-h deadline timer doesn't fire early because
  of the skew.
- Temporal heartbeat timestamps log the skew; the cluster
  tolerates up to 10 min node drift by default.

## Expected behaviour

- Temporal cluster's own clock (not the pod's) drives
  workflow timers — pod skew is cosmetic in logs.
- No duplicate schedule fires because schedule state lives in
  Temporal, not per-pod.

## Fail-the-test triggers

- Any cron workflow double-fires.
- Deadline timer trips before `workflow.Now()` matches the
  configured deadline.
- Temporal ejects the pod as unhealthy (> 10 min skew threshold
  hit faster than expected).
