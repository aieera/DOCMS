# Epic 5 — signature service: deferred security follow-ups

The Epic 5 adversarial review of the e-signature service confirmed 24 defects.
This branch **fixed the clean, high-exploitability authz/forgery/config/SSRF
findings and the revocation-forgery crypto cluster**; the items below are
**deferred on purpose** because a correct fix needs dedicated, test-driven work
(a real RFC3161 implementation, a trust-anchor/EKU policy, a parser rewrite, a
new document-fetch dependency, or a middleware principal-kind marker) that would
be unsafe to rush on a legal-signature path. Each has an in-code `SECURITY NOTE
(Epic 5 #N, TRACKED)` at the site.

## Fixed on this branch
| # | Sev | What |
|---|-----|------|
| 3 | CRIT | `RecordSignature` now verifies the per-signer signing token (constant-time) before marking signed — closes signature forgery / envelope completion by any tenant member. In-person path exempted via an operator-authorized flag. |
| 1 | CRIT | Embedded LTV **CRL signature verified** (`CheckSignatureFrom`) before trust; fail closed when no issuer. |
| 2 | CRIT | Embedded LTV **OCSP requires a non-nil issuer** so the responder signature is trust-anchored; fail closed otherwise. |
| 4 | HIGH | Live-fetched CRL signature verified against the issuer (http CDP no longer trusted blind). |
| 11 | HIGH | Signer factory **fails closed** when `SEDOC_SIGNER` is unset (no silent non-PAdES mock in prod). |
| 8/14 | HIGH | esign OAuth `authorize/token` URL overrides restricted to the provider's own domains (SSRF + client_secret exfil), validated at save + resolve. |
| 13 | HIGH | Provider-config PUT/DELETE gated on `owner`/`admin`. |
| 22 | MED | `CancelRequest` gated on request creator or admin. |
| 18 | MED | `SealCeremonyForRequest` binds the request to the document/version being sealed (no cross-doc signer attribution). |
| 19 | MED | In-person sign gated on operator = request creator or admin. |

## Deferred (tracked)
| # | Sev | Site | Why deferred / remediation |
|---|-----|------|----------------------------|
| 5 | HIGH | `pades/ltv.go` `TimestampValid` | Set from a `/DocTimeStamp` byte-scan, not a verified token. Needs real RFC3161 verification (couples with #6). |
| 6 | HIGH | `pades/tsa.go` `Stamp` | TSA response accepted without verifying the token CMS signature, `messageImprint == digest`, or nonce. Needs an RFC3161 verifier. |
| 7 | HIGH | `pades/ltv.go` `walkChain` | `ExtKeyUsageAny` + OS TLS trust store accepts any public serverAuth cert as a signer. Needs a document-signing trust anchor set (AATL) + EKU policy — a deployment config decision. |
| 10 | HIGH | `handler/seal_handler.go` | `/internal/seal*` session branch admits any member; needs an elevated-role-or-service-principal gate once the middleware exposes principal kind. |
| 12 | HIGH | `service/inperson.go` | `final_hash_sha256` trusted from the client; recompute server-side over fetched version bytes. |
| 23 | MED | `handler/handler.go` `createRequest` | No permission check on the target document (FK-existence only); needs a policy/document permission client (not currently wired into this service). |
| 9 | HIGH | `service/esign.go` `SendViaProvider` | Vendor-completed bytes materialized as a new version with no hash binding to what was sent. Needs a sent-content ↔ completed-doc hash check. |
| 15 | MED | `pades/parser.go` `fullCoverage` | Checks file-end only, not `ByteRange[0]==0` / Contents-gap alignment. Parser hardening. |
| 16 | MED | `pades/ltv.go` `ocspCoversCert` | Serial-only match ignores OCSP CertID issuer name/key hash (partially mitigated by #2's issuer requirement). |
| 20 | MED | `service/qes.go` `HandleReturn` | TSP-returned cert persisted as valid QES without chain/qualified/validity/hash-binding checks. |
| 21 | MED | `signer/dss_sidecar.go` | gRPC to the JVM signer is plaintext + unauthenticated both directions; needs mTLS. |
| 24 | LOW | `pades/cms.go` | Signer cert selected by serial alone; SignerInfo issuer DN not bound. |
| 17 | MED | `service/esign.go` | Webhook HMAC secret sourced env-only; DB-paste-flow tenants get an empty secret → completion webhooks 500. Config/plumbing fix. |
