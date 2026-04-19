# Chaos post-mortem — YYYY-MM-DD — <scenario>

Copy to `docs/chaos/runs/YYYY-MM-DD-<scenario>.md` and fill.

## Run metadata

- **Scenario:** 0X-<name>
- **Date:** YYYY-MM-DD
- **Duration:** <start> → <end> UTC
- **Cluster:** staging
- **Operator:** <GitHub handle>
- **Commit:** <sha>

## Verdict

- [ ] PASS — every expectation met, no fix needed
- [ ] CONDITIONAL — expectations met, minor fix tracked below
- [ ] FAIL — blocker; PR filed and linked

## Observations

One paragraph. What actually happened, in plain language, with
specific p99 / error-rate / recovery-time numbers from Grafana.

## Grafana

Attach screenshots of:

- The RED panel for each affected service during the run.
- The USE panel for the infra component under chaos.
- Any 5xx / error-rate spike within the blast radius.

## Fix PRs

| Issue | Root cause | Fix PR |
|---|---|---|
| | | |

## Lessons

What would we catch earlier next time? Does this scenario's
YAML need tighter pass/fail triggers? Are there observability
gaps that made diagnosis slow?

## Next drill

Date + scope for the next quarterly rerun.
