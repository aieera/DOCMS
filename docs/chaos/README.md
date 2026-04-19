# Chaos engineering

Wave 13.3. Six scenarios per spec §13.3 that every service is
expected to survive. Each scenario has a YAML descriptor under
[scenarios/](scenarios/), a Grafana-backed verification query,
and a post-mortem template.

This directory is the operator-facing half; the litmus /
chaos-mesh CRDs themselves live under
`deploy/helm/vaultdms/templates/chaos/` (Wave 13.3b — not yet
shipped).

## Scenarios

| # | Name | Duration | Impact blast | Expected behaviour |
|---|---|---|---|---|
| 1 | Pod kill | 60 min | every service, 10-min cadence | Pods restart in < 30 s; no 5xx spike |
| 2 | Network partition | 5 min | postgres primary ↔ replicas | Reads fail over to replica; writes back-pressure, no data loss |
| 3 | Clock skew | 5 min | +5 min on one node | Temporal workflows: no double-fire of cron, no stuck timer |
| 4 | Disk full | 10 min | `/var/lib/postgresql` @ 98 % | WAL archiving throttles; no Postgres crash |
| 5 | NATS disconnect | 60 s | storage pod ↔ NATS | Outbox publisher queues events; drains on reconnect |
| 6 | KMS outage | 5 min | Vault / AWS KMS | Encrypt paths fail-closed with 503; decrypt uses cached DEKs |

## Cadence

- **Per PR**: none. Chaos tests are destructive; they belong on
  staging.
- **Pre-release**: every scenario at least once, green verdicts
  pasted into the release checklist.
- **Quarterly drill**: full suite run against staging by
  oncall + SRE rotation. Post-mortem each finding; either
  file a fix PR or an exception with a follow-up deadline.

## Running a scenario (manual)

Each YAML descriptor under [scenarios/](scenarios/) is a
pattern an operator reads, then applies via the cluster's
chaos-mesh / litmus operator. The YAML is not itself CRD —
commit this layer as human-readable first, codegen the CRDs
when chaos-mesh is actually deployed (Wave 13.3b).

```bash
# 1. Read the scenario doc.
less docs/chaos/scenarios/01-pod-kill.md

# 2. Apply the chaos-mesh CRD that matches.
kubectl apply -f deploy/helm/vaultdms/templates/chaos/pod-kill.yaml

# 3. Watch the Grafana panel named in § "Verification."

# 4. Stop the experiment.
kubectl delete -f deploy/helm/vaultdms/templates/chaos/pod-kill.yaml

# 5. Write the post-mortem: copy post-mortem-TEMPLATE.md, fill,
#    commit under docs/chaos/runs/YYYY-MM-DD-<scenario>.md.
```

## DoD (from spec §13.3)

Every scenario has a PASS verdict or a tracked fix.

## Related

- [scenarios/](scenarios/) — YAML descriptors + Grafana queries
- [post-mortem-TEMPLATE.md](post-mortem-TEMPLATE.md) — fill after each run
- [tests/load/chaos/chaos-tests.sh](../../tests/load/chaos/chaos-tests.sh)
  — load-layer chaos (runs from k6, not the cluster layer)
