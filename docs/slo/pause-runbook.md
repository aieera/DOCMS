# SLO Burn / Pause Runbook

When you get paged by `SLOBurnFast*` or see `SLOBurnSlow*` in Slack.

## Fast burn (page, 2% budget in 1h)

You have ~1 hour before the budget is materially compromised.

1. **Ack the page.** Tell the channel which SLO and which service.
2. **Look at the dashboard.** Grafana → SLO Overview → pick the SLO.
   - Is the burn rate climbing or plateauing?
   - Is it localised to one tenant, one endpoint, one region?
3. **Recent deploy?** `kubectl rollout history` on the affected service.
   If the burn started within 30 min of a deploy, **roll it back first,
   then investigate.** Revert is the default.
4. **Known incident?** Check the incident channel and the platform
   status page. If it's already being handled, pile on there instead of
   opening a parallel investigation.
5. **Degrade gracefully.** If an upstream dep is sick (KMS, OpenSearch,
   Temporal) and there's a documented degradation mode for this service,
   turn it on:
   - OCR: pause new jobs, drain queue.
   - Search: serve cached results, return 503 to new queries with
     `Retry-After`.
   - Upload: return 503 to new init calls; existing uploads complete.
6. **Communicate.** Post in #incidents: what SLO, what started it,
   current burn rate, ETA to mitigation. Update every 15 min until clear.

## Slow burn (Slack, 10% budget in 6h)

You have hours. Act during business hours but don't ignore.

1. Open a ticket in the reliability project, link the alert and the
   dashboard snapshot.
2. Find the change that started the burn. `git log --since=7d` on the
   service, correlate against when the 6h rate crossed threshold.
3. Decide: fix forward, or revert? A slow burn usually means a
   sub-feature of a recent change is wrong, not the whole deploy — fix
   forward is often cheaper than a full revert.
4. Target having the burn stopped within one working day of the alert
   firing. If you can't, escalate to the service tech lead.

## Budget state transitions

The [error-budget policy](error-budget-policy.md) defines what happens
when the 30d budget crosses 50% and 20% remaining. You usually don't
have to action the transition yourself — the policy tells product and
the tech lead what to do with in-flight feature work. Your job during
burn is to stop the burn.

## Escalation

- Within 30 min of fast-burn page with no mitigation: escalate to
  platform lead.
- If burn continues past 2h with no identified root cause: page the
  service owner (architecture on-call).
- If an SLO enters Exhausted state (<20% budget): post-incident review
  is mandatory within 5 business days, even if the immediate fire is
  out.

## After the burn

1. Update the budget state on the dashboard (auto-computed, just check
   it reflects reality).
2. Open a post-mortem doc if the incident was fast-burn or if Exhausted
   state was reached.
3. If the burn exposed a missing alert or a bad SLO target, file a
   ticket to adjust — don't just move on.
