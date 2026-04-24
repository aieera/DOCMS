# Runbook 15 — Geofencing

**Owner:** Platform Security · **Last rehearsed:** *(fill on next on-call tabletop)*

## What it does
Wave 15.2 blocks or requires step-up MFA for requests based on source country or IP/CIDR allowlist, scoped per tenant / workspace / document. Enforced by `pkg/middleware.Geofence`, which calls `services/policy/internal/service.(*GeofenceService).Decide` on every authenticated HTTP request. Responses: `451` for deny, `428 + WWW-Authenticate: Step-Up` for step-up.

## Key surfaces
| Endpoint | Purpose |
|---|---|
| `GET /api/v1/admin/geofences` | List tenant policies |
| `POST /api/v1/admin/geofences` | Create policy |
| `DELETE /api/v1/admin/geofences/{id}` | Delete |
| `POST /api/v1/admin/geofences/test` | Dry-run decision for (ip, action, scope) |

## Resolver
Default production resolver is a **MaxMind GeoLite2** DB read from `VAULTDMS_GEOIP2_DB` (path). A `StaticResolver` seeded from `VAULTDMS_GEO_STATIC="cidr=cc;cidr=cc"` ships for dev / air-gapped installs. Both are wrapped by `CachingResolver` with a 60-second per-IP TTL. Lookups never log raw IPs — only country + `/24`.

See ADR 0028 for provider selection.

### Enabling the MaxMind adapter

The adapter lives in `pkg/geo/resolver_maxmind.go` behind the `maxmind` build tag so the default binary doesn't require a `.mmdb` file.

1. Build with the tag:
   ```sh
   go build -tags maxmind ./services/policy/cmd/server
   ```
2. Set the DB path. Commercial takes precedence when both are set:
   ```sh
   export VAULTDMS_GEOIP2_COMMERCIAL_DB=/var/lib/vaultdms/geoip/GeoIP2-Country.mmdb   # paid
   export VAULTDMS_GEOIP2_DB=/var/lib/vaultdms/geoip/GeoLite2-Country.mmdb             # free fallback
   ```
3. Construct the resolver from env + wrap with the caching layer:
   ```go
   inner, err := geo.NewMaxMindResolverFromEnv()
   if err != nil { // fall back to static / no-geo
       inner, _ = geo.NewStaticResolverFromEnv()
   }
   resolver := geo.NewCachingResolver(inner, 60*time.Second)
   ```
4. `Reload()` swaps the backing `.mmdb` atomically — the prior reader closes on a 5 s delay so in-flight lookups complete. Wire to a SIGHUP handler + the monthly GeoLite refresh cron.

### Running the adapter tests

CI doesn't ship a `.mmdb` (licensing + ~60 MB). Operators who want to verify locally:

```sh
export VAULTDMS_GEOIP2_TEST_DB=/path/to/GeoLite2-Country.mmdb
go test -tags maxmind ./pkg/geo/...
```
The test suite skips (not fails) when the env var is absent.

## Known gotchas
1. **Fail-closed on decider error.** If the decider returns an error, the middleware responds `503`. A silent-allow would defeat the control.
2. **Unknown country + allow-policy ⇒ deny.** If the resolver can't geo-locate an IP but the policy is `mode=allow` with a `country_codes` list, the request is denied. This is intentional (think "lock to EU"). Operators who want the opposite semantic set `mode=step_up` on the same country list.
3. **CIDR denylist always wins.** Even inside a `mode=allow` policy, a CIDR-denylist hit short-circuits to deny. This matches RFC precedence and prevents operators accidentally making a legacy deny invisible.
4. **Request IP sourcing.** `realClientIP` reads the right-most `X-Forwarded-For` hop (the one added by the trusted proxy). If Kong / ALB sits in front, make sure its `trusted_ips` list is narrow — otherwise a client can spoof their source country by injecting XFF. Upstream `pkg/gateway/waf.go` already validates this; the middleware defensively re-parses.
5. **No per-policy audit event on decision.** Every decision increments `geofence_decisions_total{mode,result}` but we do NOT emit an outbox event per request (would DoS the event pipeline). Denials are reconstructed from metrics + gateway access logs.

## Symptoms → fixes

### Legitimate user gets 451
1. Ask their public IP.
2. Call `POST /admin/geofences/test` with that IP and the policy scope.
3. Response shows `matched_policy_id` + `reason`. Patch the policy (add CIDR allowlist entry, remove country, etc.) or disable temporarily.

### `geofence_decisions_total{result="error"}` spiking
Indicates the decider is failing — most likely DB connectivity from the policy service. Check pgxpool saturation + NATS outbox drain.

### 428 returned but frontend not prompting MFA
`WWW-Authenticate: Step-Up` is the signal. The web app's axios interceptor (see `web/src/api/client.ts`) must recognise this status and redirect to the MFA challenge page. If not, the challenge flow is not yet wired — tracked in `docs/reports/WAVE_15_PROGRESS.md`.

### GeoIP DB stale
Refresh from MaxMind:
```sh
curl -o /tmp/GeoLite2-Country.tar.gz "https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=$MAXMIND_KEY&suffix=tar.gz"
tar -xzf /tmp/GeoLite2-Country.tar.gz -C /var/lib/vaultdms/geoip/
systemctl restart vaultdms-policy
```
A monthly Temporal cron is the intended home for this — tracked as Wave 15.2 follow-up.

## Temporary disable
Per-tenant feature flag: set `enabled=false` on each policy via `PATCH /admin/geofences/{id}` (endpoint: to be added; today `DELETE` + recreate). Process-global kill-switch: `VAULTDMS_GEOFENCE_ENABLED=false` on the services that mount the middleware — bypasses `Geofence(...)` entirely at startup. Use only during incident response.

## Forensics for a falsely-blocked user
1. Grep Kong / gateway access logs for the IP + 451 status code.
2. Cross-reference `X-Geofence-Reason` response header (captured in access log).
3. Call `/admin/geofences/test` to reproduce deterministically.
4. If GeoIP mis-classification is the culprit, re-run with an up-to-date MaxMind DB; MaxMind has a formal correction process.
