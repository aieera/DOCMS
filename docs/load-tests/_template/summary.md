---
date:        "2026-XX-XX"
campaign:    "loadtest-2026-XX"
sut_version: "vaultdms@<git-sha>"
helm_chart:  "vaultdms-<chart-version>"
run_by:      "<name>"
verdict:     "pending"   # pending | passed | failed
---

# Load test summary — 2026-XX-XX

> **Status: <pending|PASSED|FAILED>** — fill this in last, after every
> §16 cell below is green or red.

## Scope

- Cluster: `vaultdms-loadtest-2026-XX` (region us-east-1)
- Tenants: 100
- Documents: 100,000,000 (1M / tenant, lognormal sizes)
- Target VUs: 100,000
- Sustained: 10,000 req/s
- Burst: 50,000 req/s (×5 for 10 min)
- Hold: 1 hour
- Chaos overlay: 30 min (kills one data-plane pod every 5 min)

## Verdict per §16 SLO

| SLO                              | Target            | Measured          | Pass? |
|----------------------------------|-------------------|-------------------|-------|
| API p95                          | < 200 ms          | XX ms             | ✅/❌ |
| API p99                          | < 500 ms          | XX ms             | ✅/❌ |
| Search p95                       | < 300 ms          | XX ms             | ✅/❌ |
| Search p99                       | < 1000 ms         | XX ms             | ✅/❌ |
| Upload 10 MB p95 (scenario 11)   | < 2000 ms         | XX ms             | ✅/❌ |
| RAG ask p95                      | < 2000 ms         | XX ms             | ✅/❌ |
| Signing envelope create p95      | < 400 ms          | XX ms             | ✅/❌ |
| Hold phase error rate            | < 0.1%            | XX %              | ✅/❌ |
| Burst phase error rate           | < 1%              | XX %              | ✅/❌ |
| OCR pages/min/worker             | ≥ 1000            | XX                | ✅/❌ |
| Cross-tenant isolation           | 0 violations      | 0                 | ✅/❌ |

## Throughput

| Metric                                  | Value        |
|-----------------------------------------|--------------|
| Total journeys completed (hold)         | NNN          |
| Sustained req/s                         | NNN          |
| Burst peak req/s                        | NNN          |
| Browse-search-open share                | 70%          |
| Upload-workflow-approve share           | 15%          |
| RAG-query share                         | 10%          |
| Concurrent-signing share                | 5%           |

## Resource saturation (peak during hold)

| Service          | CPU avg | CPU peak | Mem avg | Mem peak | Network in/out | Notes |
|------------------|---------|----------|---------|----------|----------------|-------|
| gateway          |         |          |         |          |                |       |
| auth             |         |          |         |          |                |       |
| policy           |         |          |         |          |                |       |
| document         |         |          |         |          |                |       |
| storage          |         |          |         |          |                |       |
| search           |         |          |         |          |                |       |
| audit            |         |          |         |          |                |       |
| workflow         |         |          |         |          |                |       |
| notification     |         |          |         |          |                |       |
| signature        |         |          |         |          |                |       |
| connector        |         |          |         |          |                |       |
| postgres-primary |         |          |         |          |                |       |
| opensearch       |         |          |         |          |                |       |
| qdrant           |         |          |         |          |                |       |
| redis            |         |          |         |          |                |       |
| nats             |         |          |         |          |                |       |
| minio            |         |          |         |          |                |       |

## Identified bottlenecks

(One per row. Each MUST have a follow-up ticket or an explicit "no
action — within budget" decision.)

| Bottleneck                          | Evidence                          | Ticket / Decision |
|-------------------------------------|-----------------------------------|-------------------|
| —                                   |                                   |                   |

## Chaos overlay

| Pod killed             | When (offset from run start) | Recovery time | Errors observed |
|------------------------|------------------------------|---------------|-----------------|
| —                      |                              |               |                 |

## Artifacts

- `raw/mixed-protocol.json` — full k6 JSON output
- `raw/mixed-protocol.txt` — k6 stdout summary
- `raw/chaos.log` — chaos overlay log
- `grafana.pdf` — Grafana dashboard snapshot for the run window
- Helm values used: `helm-values.yaml`
- Terraform tfvars: `campaigns/2026-XX.tfvars`

## Notes from the run

(Free-form. What was surprising? What broke that you fixed mid-run?
What should the NEXT run do differently?)
