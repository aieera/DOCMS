# VaultDMS API Changelog

Versioning follows [SEMVER_POLICY.md](./SEMVER_POLICY.md). The whole
`/api/v1/*` surface shares one semver; per-endpoint versions do not
exist.

Every merged PR that touches an API surface MUST add a row below
before merge. "Touch" = route added, request field added, response
field added, status code added. Bug fixes that don't change the
contract don't need an entry.

## 1.0.0 — 2026-04-19 (unreleased)

Baseline. First numbered release. Subject to change until the
first customer pin.

### Added
- `GET  /api/v1/health`
- `POST /api/v1/auth/login`, `/logout`, `/mfa/verify`, `/mfa/recovery`,
  `/sessions/*`, `/api-keys/*`, `/saml/*`, `/oidc/*`, `/me`
- `/api/v1/admin/users`, `/admin/sso-configs`, `/admin/tenants`,
  `/admin/groups`, `/admin/share-links`, `/admin/retention-policies`,
  `/admin/settings`, `/admin/ediscovery/export`
- `/api/v1/documents/**` + version + annotation sub-trees
- `/api/v1/storage/**`, `/api/v1/search`, `/api/v1/saved-searches`
- `/api/v1/audit/**` + `/api/v1/audit/verify-integrity`
- `/api/v1/workflows/**`
- `/api/v1/notifications/**`
- `/api/v1/signatures/**`
- `/api/v1/webhooks`, `/api/v1/connectors`
- `/api/v1/compliance/holds/**`
- `/api/v1/privacy/dsr/**`, `/api/v1/privacy/verify/**`
- `/api/v1/residency/**`
- `/api/v1/shared/**` (public share-link access)
- `/api/v1/permissions/matrix`

### Response headers (all routes)
- `API-Version: 1.0.0` (stamped by `pkg/middleware.APIVersion`)
- `X-Correlation-ID` (echoed or generated)

### Changed
- Corrected the `apiKeyAuth` security scheme in `openapi.yaml`: API keys are
  sent as `Authorization: Bearer vdms_…` (HTTP bearer), not an `X-API-Key`
  header. Documentation/spec fix only — the runtime behaviour is unchanged.

### Deprecated
_None in 1.0.0._

### Removed
_None in 1.0.0._
