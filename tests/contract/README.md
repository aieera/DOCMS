# Contract tests — frontend ↔ backend path coverage

The web app expects every path in [paths.txt](paths.txt) to exist on the
backend (any response that isn't `404` is considered a pass — the goal is
to catch *missing route registrations*, not to validate business logic).

## Run against a local stack

```bash
# From the repo root
make up                                  # bring up docker compose
./tests/contract/run.sh                  # hits every path, prints table
```

`run.sh` reports FAIL if any path returns 404. Credentials are optional
(the script falls back to unauthenticated requests, which is enough to
prove the route is registered — the backend will reply 401 instead of
404 for an unauthenticated call on a valid path).

## Add a new path

1. Add the entry to [paths.txt](paths.txt) in the form `METHOD /path`.
2. Push a PR — CI runs the harness after `docker compose up` and fails
   if any path 404s.

## Current coverage

Matches the 10 previously-missing paths from
[docs/audit/05-contracts.md §3](../../docs/audit/05-contracts.md) plus
the existing auth / document / search / notification / workflow surface.
