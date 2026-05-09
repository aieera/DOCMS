# ADR 0070 — eIDAS Qualified TSP Integration

Date: 2026-05-09
Status: Accepted

## Context

§11.1 needs **Qualified Electronic Signatures** under the eIDAS
Regulation. Unlike the AdES flows the signature service already
supports (server-HSM via DSS, user-held detached CMS — see
[ADR 0025](0025-pades-library.md)), QES requires:

1. **A Qualified Signature Creation Device (QSCD)** — only a
   Qualified Trust Service Provider (QTSP) on the EU Trusted List
   may operate one.
2. **Identity verification at the QTSP**, not at us. Vetting flows
   are out of scope (national eIDs, video-ident, etc.) — we redirect.
3. **A signed hash returned by the QTSP**, embedded into the PAdES
   `/Contents` field exactly as DSS already does for AdES. The
   visible difference is the certificate chain (qualified) and the
   `signature-policy-identifier` in the signed attributes.

Three QTSPs are in scope for v1:

| TSP        | Country | Auth model                           | Sandbox base URL                          |
|------------|---------|--------------------------------------|--------------------------------------------|
| Swisscom   | CH      | OAuth2 + redirect to MobileID/SRS    | `https://ais.swisscom.com/AIS-Server/rs`  |
| Intesi Group | IT    | OAuth2 + redirect to TSA portal      | `https://signhub-pp.intesigroup.com`      |
| InfoCert   | IT      | OAuth2 + redirect to GoSign portal   | `https://api.test.infocert.it`            |

All three speak a similar three-step ceremony — exact endpoints
differ but the verbs map cleanly to a four-method interface.

## Decision

### `pkg/signing/tsp` interface

```go
type TSPClient interface {
    Register(ctx, RegisterReq)  (*RegisterResp,  error)  // create user identity
    Authorize(ctx, AuthorizeReq) (*AuthorizeResp, error) // build redirect URL + session
    Sign(ctx, SignReq)           (*SignResp,      error) // exchange auth code → signed hash
    Validate(ctx, ValidateReq)   (*ValidateResp,  error) // verify cert chain on TL
}
```

Three adapters: `swisscom.go`, `intesi.go`, `infocert.go`. A
`mock.go` adapter (deterministic, no network) is what CI + Playwright
exercise. The service-layer factory picks one by `tsp_provider` on
the signature request.

### Signing ceremony

```
[1] User picks "Qualified" + TSP on /sign UI
[2] POST /api/v1/signatures/qes/start
       → svc.StartQES → tsp.Authorize → store session row (PENDING)
       → return { redirect_url } to frontend
[3] Frontend window.location = redirect_url
[4] User authenticates at QTSP, consents
[5] QTSP redirects browser to
       /api/v1/signatures/qes/return?session=<sid>&code=<auth_code>
[6] Handler → svc.HandleReturn → tsp.Sign(auth_code, document_hash)
       → returns { signed_hash, cert_chain }
[7] Service hands signed hash + chain to the existing PAdES signer
       (signer.Mode = ModeUserHeld, DetachedSignature = signed_hash)
[8] Stores qes_certificates row, marks session COMPLETED, emits
       dms.signature.qes.completed.v1 outbox event for audit + LTV.
```

### Schema (migration 000035)

```sql
CREATE TABLE tsp_signing_sessions (
  tenant_id    UUID  NOT NULL REFERENCES organizations(id),
  id           UUID  NOT NULL DEFAULT gen_random_uuid(),
  request_id   UUID  NOT NULL,         -- FK → signature_requests
  signer_id    UUID  NOT NULL,         -- FK → signature_signers
  provider     TEXT  NOT NULL CHECK (provider IN ('swisscom','intesi','infocert')),
  status       TEXT  NOT NULL CHECK (status IN ('pending','authorized','completed','failed','expired')),
  document_hash TEXT NOT NULL,         -- hex SHA-256 of the to-be-signed PDF
  external_id  TEXT,                   -- TSP-side session/transaction id
  redirect_url TEXT NOT NULL,
  return_url   TEXT NOT NULL,          -- our callback that the TSP redirects to
  failure_reason TEXT,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  authorized_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  expires_at   TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (tenant_id, id)
);

CREATE TABLE qes_certificates (
  tenant_id    UUID  NOT NULL REFERENCES organizations(id),
  id           UUID  NOT NULL DEFAULT gen_random_uuid(),
  signer_id    UUID  NOT NULL,
  request_id   UUID  NOT NULL,
  provider     TEXT  NOT NULL,
  subject_dn   TEXT  NOT NULL,
  issuer_dn    TEXT  NOT NULL,
  serial_hex   TEXT  NOT NULL,
  not_before   TIMESTAMPTZ NOT NULL,
  not_after    TIMESTAMPTZ NOT NULL,
  cert_pem     TEXT  NOT NULL,           -- leaf cert
  chain_pem    TEXT,                     -- intermediates
  ltv_revocation JSONB,                  -- OCSP/CRL responses captured at sign time
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, id)
);
```

The existing `signature_requests.provider` CHECK is widened to
include `qes_swisscom | qes_intesi | qes_infocert`. Both new tables
are RLS-isolated on `tenant_id`.

### Audit + LTV prep

Per [ADR 0025](0025-pades-library.md), B-LT requires the validation
material captured at sign time. We persist it in
`qes_certificates.ltv_revocation` (OCSP single-response + any CRL
chunks). The signer sidecar embeds those into the PDF's DSS
dictionary on the same request — keeping LTV generation offline-safe
even if the QTSP's OCSP responder is slow later.

Audit hooks:
- session created → `dms.audit.qes.started.v1`
- TSP returned signed hash → `dms.audit.qes.signed.v1`
- embedding completed → `dms.audit.qes.embedded.v1`

All three flow through the signature-service outbox (same path as
`dms.signature.completed.v1`).

## Consequences

- We never see the signer's private key — that's the whole point of
  QES. The QSCD stays at the QTSP; we send a hash and receive a
  signed hash. This means our threat model for QES is "hash
  collision resistance + redirect-flow CSRF protection," not "key
  custody."
- The redirect leg makes the ceremony non-atomic. A user can abandon
  the flow mid-redirect; sessions auto-expire (default 15 min,
  configurable per tenant). The `expires_at` column is the
  authoritative cutoff — a stale session re-uses the QTSP-side
  transaction's expiry but our reaper drops the row.
- Three TSP adapters, three sandboxes. CI exercises the mock; the
  three real adapters need integration credentials in a sealed
  GitHub Actions env before they are exercised in CI nightly. See
  [the runbook](../runbooks/qes-signing-operations.md) for the
  credential layout and the sandbox quirks (especially Intesi's
  pinned-server certificate vs. our default trust store).
- The `return_url` is signed — we HMAC it with a per-session secret
  so the QTSP redirect cannot be tampered with by the browser. This
  is a small but important deviation from the OAuth standard
  callback shape; see `pkg/signing/tsp/state.go`.

## Out of scope

- Mobile-only signing flows (Swisscom MobileID push, FreOTP). The
  three sandboxes here all support a SMS or web-PIN fallback that we
  drive from the same `Authorize` call.
- Bulk-sign (one user, N documents) — every QTSP we surveyed
  requires per-document consent. We can batch UI prompts later but
  not server-side.
- Cross-border recognition lookup against the EU LOTL. The signer
  sidecar does that on Validate; we don't second-guess it here.
