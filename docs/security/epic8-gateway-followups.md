# Epic 8 — graphql-gateway: fixes + deferred follow-ups

The Epic 8 adversarial review of the graphql-gateway (the internet-facing API
front door) found the service **notably solid**: no tenant/identity spoof
(identity is server-derived from the verified session; Kong strips client
`X-Auth-*`), no cross-tenant DataLoader cache (loaders are request-scoped), no
APQ hash poisoning, and query depth/complexity is bounded by the **persisted-query
allowlist** (only manifest queries run). The confirmed issues were DoS hardening
and error-leakage; all are fixed here.

## Fixed on this branch
| # | Sev | What |
|---|-----|------|
| 1 | HIGH | The public `POST /api/v1/graphql` body was decoded with no size cap, and the http.Server set only `ReadHeaderTimeout` — so a multi-GB or slow-drip body could exhaust memory / pin a connection (amplified DoS). Added `http.MaxBytesReader` (1 MiB, → 413) and `ReadTimeout`/`WriteTimeout`/`IdleTimeout` on the server. |
| 2 | HIGH | The executor serialized a raw upstream gRPC error (`err.Error()`) into the client `errors` array, leaking internal topology (`dial tcp <internal-ip:port>`, backend detail) — reachable via the allowlisted `DocumentDetail` query when a backend is down. Now `clientSafeError` logs the raw error server-side and returns only a generic message keyed off the gRPC status code. |
| 3 | LOW | The panic-recovery handler echoed the recovered panic value (`stringifyPanic`) into the client `detail`. Now the full value is logged server-side and the client gets a generic "internal server error". |

## Deferred (tracked)
| Item | Sev | Site | Why deferred / remediation |
|------|-----|------|----------------------------|
| WorkflowInstance document-less view-check | LOW | `resolver/resolver.go` `WorkflowInstance` | A workflow instance with an empty `document_id` skips the gateway view-check and is returned to any tenant member who knows the id, relying on the workflow backend's own GetInstance authz. **Not currently reachable** — `workflowInstance` is not in the persisted-query manifest, and the backend is tenant-scoped (RLS). If it is ever exposed, gate document-less instances on the backend's per-user authz (or fail closed at the gateway). The workflow backend's GetInstance authz model was not verified in this pass. |
