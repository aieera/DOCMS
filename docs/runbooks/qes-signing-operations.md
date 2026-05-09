# Runbook — QES Signing Operations

Sister doc to [ADR 0070](../adr/0070-qes-tsp-integration.md).
Scope is **operating** the QES integration once it's live: rotating
credentials, debugging stuck sessions, recovering after a TSP outage.
Architecture lives in the ADR.

## Topology

```
[browser]          [signature-svc]        [QTSP]
   │  POST /qes/start   │                   │
   │ ──────────────────▶│  Authorize        │
   │                    │ ─────────────────▶│
   │  302 redirect_url  │  redirect_url     │
   │ ◀──────────────────│ ◀─────────────────│
   │                    │                   │
   │  user authenticates at QTSP            │
   │ ◀──────────────────────────────────────│
   │  GET /qes/return?session=...&code=...  │
   │ ──────────────────▶│  Sign(code, hash) │
   │                    │ ─────────────────▶│
   │                    │ ◀─signed hash─────│
   │                    │  Embed → PAdES    │
   │  302 to /sign/done │                   │
```

## Credentials

Sandbox creds live in `VAULT/secret/dms/qes/<provider>/sandbox`,
prod creds in `VAULT/secret/dms/qes/<provider>/prod`. The signature
service reads them at boot via `pkg/config`. **Never** put prod
credentials in `.env` — they're rotated quarterly and Vault has
the cron.

| Provider  | Vault path                              | Required keys                                  |
|-----------|------------------------------------------|------------------------------------------------|
| Swisscom  | `dms/qes/swisscom/{sandbox,prod}`        | `customer_id`, `client_cert_pem`, `client_key_pem` |
| Intesi    | `dms/qes/intesi/{sandbox,prod}`          | `client_id`, `client_secret`, `pinned_ca_pem` |
| InfoCert  | `dms/qes/infocert/{sandbox,prod}`        | `client_id`, `client_secret`, `org_id`        |

> **Intesi quirk**: their sandbox CA isn't in Mozilla's bundle. The
> `pinned_ca_pem` value is loaded into a custom `tls.Config.RootCAs`
> in `intesi.go`. Don't put the cert in the system trust store —
> we want it scoped to the Intesi client only.

## Common operations

### Rotate a TSP credential

1. Generate the new credential at the QTSP portal (each has a
   self-service rotation flow).
2. Write to Vault under the same key — the service reads on boot
   only, so a deploy is required.
3. Trigger a rolling restart: `kubectl rollout restart deploy/signature -n vaultdms`.
4. Watch `qes_session_create_total{result="error"}` in Grafana for
   the next ~10 min. Spike means the new cred didn't take.

### Reaper for expired sessions

```sql
UPDATE tsp_signing_sessions
   SET status = 'expired'
 WHERE status IN ('pending','authorized')
   AND expires_at < now();
```

The signature service runs this every 5 minutes — see
`reaper.go`. If you see `pending` rows older than 30 minutes, the
reaper has stalled; check the service logs for "qes reaper".

### A user is stuck mid-redirect

Symptoms: signature_request stuck `in_progress`, no
`tsp_signing_sessions` row in `completed`, the user reports a
QTSP error page.

1. `SELECT * FROM tsp_signing_sessions WHERE request_id = '...';`
2. If `status = 'failed'` → read `failure_reason`. Common: PIN
   timeout (Swisscom 60s), declined consent, expired transaction.
3. If `status = 'pending'` and the user IS at the QTSP screen, do
   nothing — the session has 15 minutes.
4. If `status = 'pending'` past `expires_at`, the reaper will mark
   it expired. The frontend handles `status: expired` by surfacing
   a "Try again" CTA — the user just restarts.

**Never** manually flip a session to `completed` — there's no
signed hash to embed, so the resulting PDF would have no QES.

### A QTSP is down

1. Health-check from a host that can reach the sandbox URL:
   ```
   curl -fsS https://<provider-base>/health
   ```
2. If the QTSP itself is down: set the maintenance flag in admin
   UI ("Disable QES via <provider>"). The signature-type selector
   on the frontend reads this and greys out the option for the
   duration. We do NOT auto-failover to a different QTSP — the
   user picked one for legal-recognition reasons.
3. Already-issued signatures keep working — verification is
   offline (LTV revocation captured at sign time).

### LTV revocation refresh

The DSS dictionary embeds the OCSP single-response captured at
sign time. For B-LTA archive-timestamping (re-stamping every 1-2
years), the signer sidecar pulls fresh OCSP/CRL responses; the
QTSP doesn't need to be involved past initial signing. See
[09-signature.md](09-signature.md) §4.

## Metrics worth watching

| Metric                                       | What it tells you                |
|----------------------------------------------|----------------------------------|
| `qes_session_create_total{provider,result}`  | Are starts succeeding?           |
| `qes_redirect_complete_total{provider}`      | How many users finish the flow?  |
| `qes_sign_latency_seconds{provider}`         | TSP-side signing latency p95     |
| `qes_session_expired_total{provider}`        | Reaper rate — high = TSP issues  |
| `qes_certificate_chain_size_bytes`           | Cert-chain payload — sudden jump means TSP changed CA layout |

## Incident severity matrix

| Symptom                                       | Severity | First action                       |
|----------------------------------------------|----------|------------------------------------|
| All starts fail, single provider              | SEV-2    | Disable QES via that provider in admin UI |
| All starts fail, all providers                | SEV-1    | Page on-call; check our outbox + DB |
| Stale `pending` sessions, reaper not catching | SEV-3    | Check reaper logs; restart svc if needed |
| Signed-hash returned but embedding fails      | SEV-2    | Sidecar issue — see [09-signature.md](09-signature.md) |
