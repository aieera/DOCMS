// Package internalauth is the single authentication plane for
// service-to-service calls on /internal/* endpoints. It replaces the
// per-service ad-hoc checks (RequireGatewaySignature alone, or none
// at all) described in tech-debt item T-D-1.
//
// Two methods are supported, selectable per-service via
// VAULTDMS_INTERNAL_AUTH_MODE={mtls,hmac,both}:
//
//   - mtls: validates the TLS peer certificate chain against the
//     internal CA (VAULTDMS_INTERNAL_CA_CERT) and requires the client
//     certificate's SAN to appear in a per-endpoint allowlist.
//
//   - hmac: verifies an X-Internal-Signature header that carries a
//     timestamp and an HMAC-SHA256 over (method | path | timestamp |
//     body), with a 5-minute clock-skew window. Shared secret from
//     VAULTDMS_INTERNAL_HMAC_SECRET.
//
//   - both: accepts either. Used during the rollout — a service
//     starts in "both", upstream callers migrate to mTLS one by one,
//     then the service flips to "mtls" only. The HMAC path is
//     deprecated on day one; it exists so operators don't have to
//     ship mTLS everywhere atomically.
//
// ADR 0031 documents the trust boundary: /internal/* is reachable
// only from in-cluster peers holding either a cert issued by the
// internal CA or the shared HMAC secret; /api/* remains session-cookie
// authenticated and unaffected by this package.
package internalauth
