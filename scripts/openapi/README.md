# OpenAPI tooling

Wave 14.2. The canonical spec lives at `docs/api/openapi.yaml`
(OpenAPI 3.1). Two things gate it:

1. **Lint** — `spectral lint docs/api/openapi.yaml` (CI).
2. **Drift** — `bash scripts/openapi/check-drift.sh` (CI, advisory
   while the backfill is in progress).

## Drift check

Greps route registrations out of `services/**/*.go` and diffs the set
of paths against the `paths:` block in the spec. Prints:

- **Undocumented** — in code, not in spec. Hard fail.
- **Stale** — in spec, not in code. Warning only unless `STRICT=1`.

The matcher is deliberately approximate — it catches `chi`, `gorilla`,
and `net/http` registrations but can miss dynamically-prefixed groups.
Treat a clean run as necessary, not sufficient.

## Current state (2026-04-18)

Code has ~237 route registrations across 38 handler files. The spec
lists 15 paths. The gap is documented in
`docs/audit/remediation/21b-wave14.2-openapi.md` — Wave 14.2 backfill
happens per-service, tracked against the scorecard in that doc.

The drift check is wired to CI as **advisory** (non-blocking) until the
first green run; after that it becomes a hard gate. This matches the
mutation-test rollout pattern — ship the rail, flip to enforcing after
first green.

## Running locally

```bash
bash scripts/openapi/check-drift.sh
# strict mode fails on stale paths too
STRICT=1 bash scripts/openapi/check-drift.sh
```

## Rendered docs

Published to `docs.vaultdms.io/api` via a separate static-site build
(Wave 14.2b). The spec source of truth is the YAML; rendered pages
rebuild on every merge to main.
