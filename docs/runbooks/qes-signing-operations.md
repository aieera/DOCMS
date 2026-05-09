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

## Sandbox smoke harness

Nightly GitHub Actions workflow at
[.github/workflows/esign-sandbox.yml](../../.github/workflows/esign-sandbox.yml)
runs the **Tier A** tests in
[pkg/esign/sandbox_test.go](../../pkg/esign/sandbox_test.go) +
[pkg/signing/tsp/sandbox_test.go](../../pkg/signing/tsp/sandbox_test.go)
against the live vendor sandboxes. Tier A is wire-shape only — it
sends an envelope or opens a QES Authorize transaction, polls
status / asserts the redirect URL came back, and cleans up. No
human in the loop. Tier B (full round-trip with API-driven
signing for DocuSign + Adobe Sign) is opt-in via `workflow_dispatch`
with `tier=b`.

### Where the credentials live

GitHub Environment **`esign-sandbox`** — only `main` is allowed to
use it; PR branches never see the secrets. The workflow's `env:`
blocks list the exact secret names; each one is documented inline
in the corresponding `sandbox_test.go`. To add a new vendor, the
shape is:

1. Add the env vars to `sandbox_test.go`'s file-level comment.
2. Add `mustEnvSandbox(t, "...")` calls in the new test.
3. Wire the secret into the workflow's two `env:` blocks (Tier A +
   Tier B if applicable).

### Reading a failed run

Look at the failing test name first:

| Test name suffix      | What broke                                              |
|-----------------------|---------------------------------------------------------|
| `_TierA`              | Wire shape changed at the vendor, or a credential expired. Compare the test's request body with the vendor's current docs. |
| `_TierB_FullCycle`    | Wire-shape OK, but auto-signing failed or the signed PDF didn't materialize. Most often: vendor changed their auto-sign API, or our `assertLooksLikePDF` is too strict. |

If a single vendor fails but the others pass, that's almost always
a credential-rotation issue at that vendor. Re-issue the dev token
or refresh-token grant from the vendor portal and update the
GitHub Environment secret. **Do not** disable the failing test —
write a tracking ticket and let the workflow stay red until the
underlying issue is fixed; the red signal is the value.

### Local run

```
DOCUSIGN_SANDBOX_ACCESS_TOKEN=... \
DOCUSIGN_SANDBOX_ACCOUNT_ID=... \
DOCUSIGN_SANDBOX_BASE_URI=https://demo.docusign.net \
DOCUSIGN_SANDBOX_SIGNER_EMAIL=you@example.com \
  go test -tags sandbox -v -run TestSandbox_DocuSign_TierA ./pkg/esign/...
```

Without the env vars the tests skip cleanly (`mustEnv` calls
`t.Skipf`); they don't fail the default `make test` run.

## Incident severity matrix

| Symptom                                       | Severity | First action                       |
|----------------------------------------------|----------|------------------------------------|
| All starts fail, single provider              | SEV-2    | Disable QES via that provider in admin UI |
| All starts fail, all providers                | SEV-1    | Page on-call; check our outbox + DB |
| Stale `pending` sessions, reaper not catching | SEV-3    | Check reaper logs; restart svc if needed |
| Signed-hash returned but embedding fails      | SEV-2    | Sidecar issue — see [09-signature.md](09-signature.md) |
