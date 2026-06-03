# SeDoc REST API — versioning & deprecation policy

**Blueprint §:** 12.1 · **Applies from:** `v1.0.0` · **Status:** enforced

## The contract

Every public HTTP response from `/api/v1/*` sets these headers:

| Header | Value | Notes |
|---|---|---|
| `API-Version` | `1.0.0` (or the rolled semver) | Included on every response so clients can pin. |
| `Deprecation` | RFC 9745 date — `Sun, 01 Jan 2027 00:00:00 GMT` | Only on deprecated routes. |
| `Sunset` | RFC 9745 date | When the route stops working. MUST be ≥ 6 months after `Deprecation`. |
| `Link` | `<https://docs.vaultdms.example/migrations/...>; rel="deprecation"` | Pointer to migration notes. |

Consumers SHOULD read `API-Version` and surface mismatches to the end user.
Consumers MUST NOT pin to a specific minor; only the major (`v1`) is stable.

## Version bumps

Semver over the whole `/api/v1/*` surface — NOT per-endpoint:

- **PATCH** (`1.0.x → 1.0.y`): bug fix, no field added / removed / retyped.
- **MINOR** (`1.x → 1.y`): field added, new route added, new status code added
  for an existing route, new optional query parameter. Backward compatible.
- **MAJOR** (`1.x → 2.x`): ANY of:
  - field removed or renamed
  - field type changed
  - required request field added
  - route removed or its path changed
  - response-status semantics changed (e.g. 200 → 202 for the same request shape)
  - authentication / authorization class raised (e.g. `required` → `admin`)

A MAJOR bump requires:
1. New path prefix `/api/v2/` served IN PARALLEL with `v1` for ≥ 12 months.
2. Every `v1` response gets a `Deprecation` header as of the v2 release date.
3. `Sunset` header = v2 release date + 12 months.
4. Changelog entry in `docs/api/CHANGELOG.md` with the breaking diff.
5. A new `routes.yaml` entry for the v2 prefix in `deploy/gateway/`.

## Gating at CI

- `proto-breaking` GHA job runs `buf breaking` against the previous
  release tag — a breaking proto change fails the build unless the
  PR bumps the major version.
- Archtest `TestAPIVersionHeaderSetOnAllRoutes` (below) asserts every
  handler set `API-Version` before returning. Missing header =
  regression.

## Deprecation runbook

1. Open an issue labelled `api-deprecation` with the route + the new
   replacement.
2. Add the route to `docs/api/DEPRECATIONS.md` with the date schedule.
3. Add the `Deprecation` + `Sunset` + `Link` headers in the handler
   immediately. Do NOT wait for the sunset date to start warning
   clients.
4. Announce on status page + customer email at least 6 months before
   sunset.
5. On the sunset date: the handler returns HTTP 410 Gone with a
   pointer to the replacement; the code path is kept one release
   past sunset for diagnostics, then removed.

## Header implementation

The middleware `pkg/middleware/api_version.go` (landed in this PR)
adds `API-Version` to every response. Handlers that need to add
`Deprecation`/`Sunset`/`Link` call the `middleware.Deprecate(...)`
helper — one line per route.

## Never

- Never change a response field's type without a major bump, even if
  "no one was using it."
- Never delete a route with less than 6 months sunset, even for
  security-hygiene reasons — instead, 401 / 403 / 410 the route
  immediately and keep the handler shell so clients get a
  machine-readable signal.
- Never version individual endpoints (e.g. `/api/v1/docs/v2`). Bump
  the top-level prefix or don't bump.
