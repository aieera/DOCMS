# ADR 0028 — GeoIP provider selection for Wave 15.2 geofencing

* Status: Accepted
* Date: 2026-04-21
* Wave: 15.2

## Context

Wave 15.2 enforces per-tenant country / CIDR policies on every authenticated request. This requires an IP-to-country-code resolver on the hot path. The lookup MUST be:

1. Sub-millisecond p99 (it runs on every request).
2. Usable air-gapped (no network call allowed in on-prem installs).
3. Refreshable without a deploy.
4. Legally OK to redistribute in our container images for dev.

Options considered:

- **MaxMind GeoLite2** (free, CC-BY-SA-4.0, monthly refresh, must sign an EULA and include attribution).
- **MaxMind GeoIP2 Commercial** (paid, same Go reader, higher accuracy, no EULA friction for customers).
- **ipapi.co / ip-api.com** (online API). Rejected — violates air-gap requirement, adds 30-200 ms latency per request, rate limits.
- **Static CIDR→country table** (operator-provided). Workable for small deploys / CI but not at scale.
- **ipinfo.io IPinfo Lite** (free with attribution, newer entrant). Promising but smaller operational track record.

## Decision

Ship a `pkg/geo.Resolver` interface with two first-class implementations and a third deferred:

1. **StaticResolver** (`pkg/geo/resolver.go`) — parses `VAULTDMS_GEO_STATIC="cidr=cc;…"`. Primary use: CI, dev, and air-gapped installs that don't need real GeoIP.
2. **CachingResolver** — wraps any resolver with a 60 s per-IP TTL cache so even the static path amortises correctly.
3. **MaxMindResolver** (deferred to follow-up PR, see "Follow-ups" below) — reads a `.mmdb` file pointed to by `VAULTDMS_GEOIP2_DB`. Uses `github.com/oschwald/maxminddb-golang` (or `geoip2-golang`). Behind a build tag `maxmind` so the dep doesn't bloat the binary when unused.

Why split the MaxMind impl into a follow-up PR:
- MaxMind's Go reader is a new third-party dep. Wave 15 constraint: "NO new third-party deps without an ADR." This ADR authorises it but the PR that vendors the dep should be reviewable in isolation.
- MaxMind GeoLite2 requires a license key (free signup) for downloads — redistributing the `.mmdb` from our registry is allowed per the CC-BY-SA-4.0 but adds compliance weight. We want that discussion in the follow-up PR.

Commercial GeoIP2 is drop-in — the same reader accepts the commercial DB. Tenants that require it set `VAULTDMS_GEOIP2_COMMERCIAL_DB` instead; the resolver prefers commercial when both env vars are set.

## Consequences

**Positive**
- On-prem installs work with just the static resolver, no external dep.
- Air-gapped tarball ships the GeoLite2 DB alongside the service binary; refresh via `dms-installer update --geoip`.
- Caching resolver is transparent — every Resolver gets cache semantics for free.
- Interface swap means migrating providers later (e.g. to IPinfo Lite) is a 20-line change.

**Negative**
- GeoLite2 is less accurate than the commercial product (~5-10 % mismatch on cellular/mobile ranges). Policies that depend on blocking a specific country for compliance reasons should start in `mode=step_up` and be promoted to `mode=deny` only after baseline accuracy is measured per tenant.
- Monthly refresh is operator-driven today. Until a Temporal cron ships (tracked in WAVE_15_PROGRESS), a stale DB can silently drift classification.

**Neutral**
- Commercial GeoIP2 is a runtime swap, not a rebuild. No architectural lock-in.

## Alternatives rejected

1. **Call an online API per request.** Fails air-gap; latency unacceptable.
2. **DB lookup against a Postgres table of CIDRs.** We considered seeding `geofence_policies` itself from a GeoIP dump. Rejected: duplicates data, loses MaxMind's lookup structure (~200 ns radix lookup vs. multi-millisecond index scan).
3. **eBPF-level blocking at the kernel.** Out of scope — app-layer control needed because policy is tenant-scoped and evaluated post-auth.

## Follow-ups

- **MaxMindResolver impl** behind `//go:build maxmind` — vendors `github.com/oschwald/maxminddb-golang`, reads the `.mmdb` file, implements `geo.Resolver`. Separate PR.
- **Temporal cron** to refresh the `.mmdb` monthly + signal the policy service to reload.
- **`geofence_geoip_db_age_seconds` Prom gauge** so operators alert on stale DBs.
- **Operator doc** covering the MaxMind EULA + attribution requirements.
