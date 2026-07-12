# Runbook: SAML SP certificate — pinning & rotation

## Background

The auth service is a SAML **Service Provider (SP)**. Its SP key+cert
sign AuthnRequests and are published in SP metadata; many IdPs **pin**
the SP certificate and reject requests signed by an unrecognized cert.

Previously the SP key+cert were regenerated on **every boot**, so the SP
cert changed on each restart and pinned IdPs broke intermittently
(outages that looked like IdP flakiness). The SP identity is now
**durable**: generated once and reused across restarts.

## Where the identity comes from (precedence)

1. **Operator-injected PEMs** — if `SEDOC_SAML_SP_KEY_PEM` and
   `SEDOC_SAML_SP_CERT_PEM` are set (e.g. from a K8s secret or a
   Vault-agent-rendered file), those are used verbatim. Rotation is
   whatever manages that secret.
2. **App-managed keypair** — otherwise the service generates the keypair
   **once on first boot** and persists it in the `saml_sp_keypair`
   table (private key sealed under the deployment KEK, cert in clear).
   Every subsequent boot loads the same keypair.
3. **Ephemeral (dev only)** — if there is no KEK to seal a durable key
   and no injected PEMs, a self-signed cert is generated per boot and a
   loud warning is logged. **Do not pin this.** Set `SEDOC_LOCAL_KEK`
   (or inject PEMs) to get a stable cert.

The resolved certificate **fingerprint** (SHA-256, colon-separated hex)
is logged at startup — grep `sp_cert_fingerprint` to confirm it is
stable across restarts.

## Pinning the SP cert at the IdP

The SP cert is published in the metadata endpoint the IdP admin already
consumes:

```
GET /api/v1/auth/sso/saml/{tenant_slug}/metadata
```

The `<KeyDescriptor use="signing">` element carries the SP certificate.
Point the IdP at this metadata URL (or paste the cert) and enable SP
signature validation. The fingerprint in the metadata now matches the
one logged at startup and stays constant across restarts.

## Rotation (controlled change)

Rotation is deliberately a manual, operator-driven step — never a
side-effect of a restart.

**App-managed keypair:**
1. Rotate the persisted keypair. Either run the rotation via the admin
   tooling (`RotateSPKeyMaterial`, exposed as `dms-admin saml rotate-sp`
   when wired) or, as a break-glass, clear the row so the next boot
   regenerates:
   ```sql
   -- break-glass: next auth boot generates + persists a NEW keypair
   DELETE FROM saml_sp_keypair;
   ```
   Rotation produces a NEW fingerprint (verify in the startup log).
2. **Before** the old cert stops being served, update every pinned IdP
   with the new cert (from the metadata endpoint). SAML has no built-in
   overlap for a single active SP cert, so schedule rotation in a
   maintenance window and re-pin promptly.
3. Confirm a test SP-initiated login succeeds against each IdP.

**Injected PEMs:** rotate the underlying secret (K8s/Vault), roll the
auth pods, then re-pin at the IdPs. Same re-pin ordering applies.

## Verifying

- Startup log shows the same `sp_cert_fingerprint` before and after a
  restart (no rotation).
- After a rotation, the log shows a **different** fingerprint and
  `rotated_at` is set on the `saml_sp_keypair` row.
- SP metadata's signing cert fingerprint equals the startup log value.
