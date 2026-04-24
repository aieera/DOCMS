# ADR 0031 — Internal auth plane and trusted-proxy boundary

- **Status:** Accepted · 2026-04-24
- **Closes:** T-D-1 (`/internal/*` sweeper auth on shared gateway
  secret), T-D-2 (`realClientIP` trusts right-most XFF).
- **Related:** ADR 0027 (acknowledgement hash chain), ADR 0028 (geo-IP
  provider), B9 of `docs/security/threat-model.md`.

## Context

Two tech-debt items had the same root cause — no agreed trust boundary
for calls between in-cluster peers.

**T-D-1.** The acknowledgement sweeper endpoint and several other
`/internal/*` routes were gated only by `middleware.RequireGatewaySignature`,
which verifies a single shared secret (`VAULTDMS_GATEWAY_SECRET`) held
by every Go service. A compromised or misbehaving peer could POST to
`POST /internal/v1/acknowledgement/sweep-reminders` with an arbitrary
`X-Auth-Tenant-ID` and trigger tenant-scoped side effects: reminder
emails, escalation timestamps, hash-chain appends. The gateway secret
authenticates "came through Kong," not "is the Temporal worker."

**T-D-2.** Four middlewares (geofence, request log, rate limiter, ack
handler) each reimplemented `realClientIP` with incompatible, all wrong
logic:

- `pkg/middleware/geofence.go` — right-most hop unconditionally.
- `pkg/middleware/requestlog.go` — **left-most** hop (attacker-supplied).
- `pkg/gateway/ratelimit.go` — left-most hop (rate-limit bypass).
- `services/acknowledgement/internal/handler/handler.go` — duplicate
  right-most-hop copy.

None consulted a trusted-proxy allowlist. If any of these middlewares
were ever mounted outside Kong, `X-Forwarded-For` would be
client-controlled. The ledger flagged geofence specifically; the other
three were latent.

## Decision

Introduce two orthogonal packages.

### 1. `pkg/internalauth`

Single authentication plane for `/internal/*` endpoints. Config
selects a `Mode` per service:

- **`mtls`** — TLS peer certificate chain validated against
  `VAULTDMS_INTERNAL_CA_CERT`; the client certificate's DNS SAN must
  appear in `VAULTDMS_INTERNAL_SAN_ALLOWLIST`. Empty allowlist fails
  closed so a misconfigured service cannot accept every cert the CA
  ever issued.
- **`hmac`** — `X-Internal-Signature: t=<unix>;s=<hex>` where `hex =
  HMAC-SHA256(VAULTDMS_INTERNAL_HMAC_SECRET, method ‖ LF ‖ path ‖ LF ‖
  timestamp ‖ LF ‖ body)`. 5-minute skew window. Method + path binding
  means a captured signature cannot be replayed against a different
  endpoint.
- **`both`** — mTLS attempted first when a peer cert is present;
  otherwise HMAC. **A bad peer cert does NOT fall through to HMAC** —
  a cert that fails validation is a stronger negative signal than no
  cert at all; falling through would enable a trivial bypass for an
  attacker holding the HMAC secret.

`internalauth.Mux(inner, verifier, fallback)` is a path-aware
`http.Handler`:

- `/healthz`, `/readyz`, `/metrics` → bypass (kube probes).
- `/internal/*` → `verifier.RequireInternal`.
- everything else → fallback (typically the existing
  `middleware.RequireGatewaySignature`; `/api/*` is unaffected).

A nil verifier collapses to the prior behaviour (fallback on every
non-probe path) so services compile and run identically until an
operator sets `VAULTDMS_INTERNAL_AUTH_MODE`. This is the rollout
mechanism: provision certs + flip the flag per service without an
atomic cluster-wide change.

Prometheus: `internal_auth_total{method,outcome}` on every request.
Outcomes include `mode_mismatch` and `missing` so cutover progress
is visible.

### 2. `pkg/trustedproxy`

`RealClientIP(r *http.Request, cfg Config) netip.Addr` walks XFF
right-to-left. It believes a hop only when the hop's *immediate
predecessor* (the hop on its right, or `r.RemoteAddr` for the rightmost
entry) is in `cfg.TrustedCIDRs`. The first untrusted hop is the real
client. If the peer itself is untrusted, XFF is ignored entirely.

Config loads from `VAULTDMS_TRUSTED_PROXY_CIDRS`.
`VAULTDMS_ENV=production` with an empty list panics at boot — a
misconfigured deploy must fail loudly rather than silently trust
spoofed headers.

All four prior callers (geofence, request log, rate limiter, ack
handler) now delegate here.

## Trust-boundary diagram

```
                                   Internet
                                      │
                    ┌─────────────────┴──────────────────┐
                    │   Kong gateway  (trusted proxy)    │
                    │   - terminates TLS                 │
                    │   - adds X-Forwarded-For           │
                    │   - signs X-Gateway-Signature      │
                    └─────────────────┬──────────────────┘
                                      │
                  ┌───────────────────┼───────────────────┐
                  │                   │                   │
                  ▼                   ▼                   ▼
           /api/v1/*            /internal/v1/*       /healthz, ...
      RequireGatewaySignature   internalauth.Mux    (no auth)
      SessionAuth               + RequireInternal
      (cookie → auth.User)      (mTLS or HMAC)
                                      ▲
                                      │
           ┌──────────────────────────┴───────────────────────┐
           │  Internal callers:                               │
           │   - Temporal workers (SAN: worker.temporal...)   │
           │   - Ack sweeper      (SAN: sweeper.ack...)       │
           │   - Other Go services (SAN: <svc>.internal)      │
           └──────────────────────────────────────────────────┘
```

## Consequences

**Positive.**

- Holding `VAULTDMS_GATEWAY_SECRET` no longer grants lateral access to
  `/internal/*`. Attacker needs a cert issued by the internal CA with
  a SAN in the target service's allowlist, or the distinct internal
  HMAC secret.
- Single source of truth for client IP. Rate limiting, audit
  logging, geofencing, and per-tenant metrics all see the same value.
- Rollout is opt-in per service. `VAULTDMS_INTERNAL_AUTH_MODE=both`
  lets callers migrate caller-by-caller before the service flips to
  `mtls`.

**Negative / risk.**

- Operational surface grows: a new CA to rotate, per-service leaf
  certs (managed by cert-manager; 30-day lifetime, auto-renew),
  `VAULTDMS_TRUSTED_PROXY_CIDRS` must be kept current when the gateway
  topology changes.
- Incorrect SAN allowlist configuration fails closed. Operators must
  follow the bootstrap runbook (`docs/runbooks/internal-mtls-bootstrap.md`)
  when adding a new internal caller.
- Test harnesses that previously constructed requests with arbitrary
  `X-Forwarded-For` and expected the middleware to honour the
  right-most hop now need to configure a trusted-proxy CIDR matching
  the mock `RemoteAddr`. `pkg/middleware/geofence_test.go` is updated;
  downstream tests that hit geofenced routes may need the same change.

## Alternatives considered

1. **SPIFFE/SPIRE.** Bigger lift than needed today; cert-manager is
   already in the cluster for public TLS, and service identities here
   are small and well-known.
2. **mTLS-only, no HMAC.** Would require cert+secret rollout to every
   caller atomically (including non-Go services: Temporal workers,
   Python intelligence, Node collaboration). The HMAC fallback buys
   us per-service cutover and is deprecated in the same ADR.
3. **Keep `RequireGatewaySignature` everywhere, add IP allowlist.** An
   IP allowlist is brittle in Kubernetes (pod CIDRs rotate) and doesn't
   address the "any peer holding the secret" class of threat.
