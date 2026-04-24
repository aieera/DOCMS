# Runbook 15 — Chaos drill execution

Operator procedure for running the 7 chaos scenarios under
[docs/chaos/scenarios/](../chaos/scenarios/) against a staging
cluster, on a **quarterly cadence**. Pairs with `docs/chaos/drill-YYYY-MM-DD.md`
which captures the run output.

## Prerequisites

1. **Staging cluster** with chaos-mesh installed in the `chaos-mesh` namespace. Confirm:
   ```bash
   kubectl get pods -n chaos-mesh
   # → chaos-controller-manager, chaos-daemon DaemonSet, chaos-dashboard
   ```
2. **Grafana access** — the runbook references screenshots from these dashboards:
   - Platform overview ([deploy/monitoring/dashboards/platform-overview.json](../../deploy/monitoring/dashboards/platform-overview.json))
   - Event pipeline ([deploy/monitoring/dashboards/event-pipeline.json](../../deploy/monitoring/dashboards/event-pipeline.json))
   - Per-service health ([deploy/monitoring/dashboards/per-service-health.json](../../deploy/monitoring/dashboards/per-service-health.json))
3. **k6 running background load** during each scenario — otherwise "service recovered" is trivially true when no traffic is hitting it. Use `make -C tests/load mixed` for a steady 10 K user simulation throughout the drill.
4. **On-call primary NOT on duty during the drill window** — you WILL page them during clock-skew (scenario 03) if done wrong.

## Cadence

- **Quarterly** — first Tuesday of every quarter, 14:00 UTC (lowest-traffic window per load dashboards).
- **Duration** — allow 3 hours end-to-end. Scenarios 01/02/05 are ≤15 min each; scenarios 03/04/06/07 each have longer verification windows.
- **Triggered by** — ops calendar (PagerDuty schedule or equivalent). No GH Actions cron suggested: scheduled workflows can't inject into a staging cluster without credentials, and quarterly manual kick-off keeps a human in the loop for abort.

## Execute per scenario

Each scenario's markdown has Setup + Trigger + Verification. Copy-paste as-is; don't improvise the CRDs.

```bash
# 1. Start background load.
make -C tests/load mixed &  # writes results/<timestamp>-mixed.json
SLEEP=30; sleep $SLEEP      # let metrics stabilise before first kill

# 2. Run each scenario in series — NOT parallel. Parallel chaos
#    muddles the verification: a Grafana spike during scenario 2
#    could be residual from scenario 1.

for s in 01-pod-kill 02-network-partition 03-clock-skew 04-disk-full \
         05-nats-disconnect 06-kms-outage 07-ocr-worker-kill; do
  echo "=== Scenario $s ==="
  read -p "Apply CRD from docs/chaos/scenarios/${s}.md? [y/N] " go
  [ "$go" != "y" ] && continue
  # Apply the scenario's PodChaos / NetworkChaos / TimeChaos / IOChaos
  # CRD from the markdown. Each scenario names the kubectl apply it
  # expects — copy/paste rather than auto-extract.
  # Capture Grafana PNGs before / during / after:
  ts=$(date +%Y%m%d-%H%M%S)
  curl -sG 'https://grafana.staging.vaultdms/render/d/platform-overview' \
       -d 'width=1400' -d 'height=800' -d 'tz=UTC' \
       > docs/chaos/profiles/${ts}-${s}-platform.png
  # ... run the scenario, wait the documented verification window,
  # copy the 3 Grafana screenshots into docs/chaos/profiles/.
done

# 3. Stop background load.
kill %1
```

## Record results

Populate the drill result doc (stub auto-created for the drill date):

1. For each scenario row: Observed = bullet list of what actually happened. Compare to the scenario's "Expected behaviour" section.
2. Verdict column:
   - **PASS** — every assertion in the scenario's Verification section held AND no `Fail-the-test triggers` fired.
   - **FAIL** — any verification assertion failed OR a fail-trigger fired.
   - **N/A** — scenario skipped (document why in the row).
3. For every FAIL: file a fix PR, link from the row. Same DoD rule as performance baseline — a FAIL without a linked PR blocks the drill doc merge.
4. Attach Grafana screenshots under `docs/chaos/profiles/YYYY-MM-DD/`.
5. Post-mortem per scenario that required a fix: use [docs/chaos/post-mortem-TEMPLATE.md](../chaos/post-mortem-TEMPLATE.md).

## Aborting a drill

Two exit mechanisms:

```bash
# Kill every chaos-mesh experiment in the namespace:
kubectl delete -n vaultdms podchaos --all
kubectl delete -n vaultdms networkchaos --all
kubectl delete -n vaultdms timechaos --all
kubectl delete -n vaultdms iochaos --all

# Nuclear: pause the whole chaos-mesh controller.
kubectl -n chaos-mesh scale deploy chaos-controller-manager --replicas=0
# (Re-scale to 1 after investigating.)
```

Any scenario that bleeds past its documented duration → abort immediately.
**Do not debug during the drill window** — fix PRs are post-drill work.

## Caveats from code

- **Scenario 05 (NATS disconnect)** — the outbox publisher at
  [pkg/database/outbox_publisher.go](../../pkg/database/outbox_publisher.go) polls every 100 ms. A 60 s NATS outage should drain cleanly once NATS returns; the
  `outbox_publish_lag_seconds` histogram added 2026-04-19 now reports
  catch-up time. Watch that metric on [event-pipeline.json](../../deploy/monitoring/dashboards/event-pipeline.json).
- **Scenario 06 (KMS outage)** — LocalKeyManager has no external dep,
  so dev-cluster KMS outage simulation is synthetic (toggle a feature
  flag that forces KeyManager errors). AWS KMS / Vault Transit
  adapters are stubbed; real KMS outage behaviour against those is
  untested until the production adapters ship.
- **Scenario 03 (clock skew)** — Temporal workflows that sleep on wall
  clock (retention, deprovision 30-day) are the actual targets.
  Verify `time.Now()` inside workflow code is replaced with
  `workflow.Now(ctx)` before skewing; violations will trigger
  determinism panics and mask the actual clock-skew behaviour.
